package ceremony

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/drand/drand/v2/common/chain"
	drandcrypto "github.com/drand/drand/v2/crypto"
	"github.com/drand/kyber/util/random"
)

func testBeacon(t *testing.T, at time.Time) (BeaconPlan, BeaconProof) {
	t.Helper()
	scheme := drandcrypto.NewPedersenBLSUnchainedG1()
	secret := scheme.KeyGroup.Scalar().Pick(random.New())
	info := chain.Info{PublicKey: scheme.KeyGroup.Point().Mul(secret, nil), ID: "test-only", Period: time.Second, Scheme: scheme.Name, GenesisTime: at.Unix() - 99, GenesisSeed: []byte("synthetic test network")}
	b, err := json.Marshal(info)
	must(t, err)
	p, err := NewBeaconPlan(b, info.HashString(), 100)
	must(t, err)
	sig, err := scheme.AuthScheme.Sign(secret, scheme.DigestBeacon(signedBeacon{round: 100}))
	must(t, err)
	return p, BeaconProof{Round: 100, Signature: hex.EncodeToString(sig)}
}

func TestCloseAndVerifiedFinalization(t *testing.T) {
	s := demoStore(t)
	// Rehearsals may start prepared to exercise the same planned-beacon path.
	cp, _, err := s.checkpoint()
	must(t, err)
	cp.Lifecycle.Phase = "prepared"
	ref, err := s.putJSON(cp)
	must(t, err)
	must(t, WriteJSON(filepath.Join(s.Dir, "checkpoint.json"), ref))
	now := time.Now().UTC().Truncate(time.Second)
	plan, proof := testBeacon(t, now.Add(2*time.Hour))
	s.now = func() time.Time { return now }
	_, err = s.OpenCeremony(plan)
	must(t, err)
	if _, err = s.OpenCeremony(plan); err == nil {
		t.Fatal("beacon plan was replaceable after opening")
	}
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	for _, track := range []string{Core, Large} {
		i, e := s.InviteQueued(server.URL, "alice", track, time.Hour)
		must(t, e)
		c := NewClient(t.TempDir())
		c.Progress = io.Discard
		must(t, c.Run(context.Background(), i, track))
		renewed, e := s.Renew(i.Token, i.Jobs[0].Circuit.Spec.ID, time.Minute)
		must(t, e)
		if !renewed.Done {
			t.Fatal("accepted contribution heartbeat did not complete")
		}
	}
	_, err = s.CloseCeremony()
	must(t, err)
	if _, err = s.Invite(server.URL, "late", Core, time.Hour); err == nil {
		t.Fatal("closed ceremony issued an invitation")
	}
	if _, err = s.Finalize(filepath.Join(t.TempDir(), "early"), proof, 64); err == nil {
		t.Fatal("finalized before planned beacon time")
	}
	s.now = func() time.Time { return plan.RoundTime.Add(time.Second) }
	bad := proof
	bad.Round++
	if _, err = s.Finalize(filepath.Join(t.TempDir(), "bad"), bad, 64); err == nil {
		t.Fatal("accepted a different beacon round")
	}
	out := filepath.Join(t.TempDir(), "final")
	final, err := s.Finalize(out, proof, 64)
	must(t, err)
	must(t, s.Audit(Core, 64))
	must(t, s.VerifyExport(out, 64))
	repeated, err := s.Finalize(out, proof, 64)
	must(t, err)
	if repeated != final {
		t.Fatal("finalization was not idempotent")
	}
	archive := testArchive{dir: t.TempDir()}
	s.Remote = archive
	checkpoint, err := s.Sync(context.Background())
	must(t, err)
	restoredDir := filepath.Join(t.TempDir(), "restored")
	must(t, Restore(context.Background(), archive, restoredDir, s.ManifestArtifact, checkpoint))
	restored, err := Open(restoredDir)
	must(t, err)
	recoveredKeys := filepath.Join(t.TempDir(), "keys")
	must(t, restored.MaterializeExport(recoveredKeys))
	must(t, restored.VerifyExport(recoveredKeys, 64))
	var metadata ExportManifest
	must(t, ReadJSON(filepath.Join(out, "export.json"), &metadata))
	signature, err := hex.DecodeString(metadata.BeaconProof.Signature)
	must(t, err)
	signature[0] ^= 1
	metadata.BeaconProof.Signature = hex.EncodeToString(signature)
	must(t, WriteJSON(filepath.Join(out, "export.json"), metadata))
	if err = s.VerifyExport(out, 64); err == nil {
		t.Fatal("accepted a tampered beacon signature")
	}
}

func TestQueueReservationsAndRenewal(t *testing.T) {
	s := demoStore(t)
	a, err := s.InviteQueued("http://localhost", "alice", Core, time.Hour)
	must(t, err)
	b, err := s.InviteQueued("http://localhost", "bob", Core, time.Hour)
	must(t, err)
	first, err := s.Claim(a.Token, time.Minute)
	must(t, err)
	again, err := s.Claim(a.Token, time.Minute)
	must(t, err)
	if first.Job.Circuit.Spec.ID != again.Job.Circuit.Spec.ID {
		t.Fatal("one participant received concurrent reservations")
	}
	second, err := s.Claim(b.Token, time.Minute)
	must(t, err)
	if first.Job.Circuit.Spec.ID == second.Job.Circuit.Spec.ID {
		t.Fatal("two participants reserved the same head")
	}
	if _, err = s.Renew(b.Token, first.Job.Circuit.Spec.ID, time.Minute); err == nil {
		t.Fatal("renewed another contributor's lease")
	}
	renewed, err := s.Renew(a.Token, first.Job.Circuit.Spec.ID, 2*time.Minute)
	must(t, err)
	if !renewed.Expires.After(first.Expires) {
		t.Fatal("lease did not renew")
	}
	l, err := s.lease(a.Token, first.Job.Circuit.Spec.ID)
	must(t, err)
	l.Expires = time.Now().Add(-time.Minute)
	must(t, WriteJSON(s.leasePath(first.Job.Circuit.Spec.ID), l))
	c, err := s.InviteQueued("http://localhost", "carol", Core, time.Hour)
	must(t, err)
	reclaimed, err := s.Claim(c.Token, time.Minute)
	must(t, err)
	if reclaimed.Job.Circuit.Spec.ID != first.Job.Circuit.Spec.ID {
		t.Fatal("expired reservation was not reclaimed")
	}
}

func TestBeaconDeadlineRejectsFurtherWork(t *testing.T) {
	s := demoStore(t)
	cp, _, err := s.checkpoint()
	must(t, err)
	cp.Lifecycle.Phase = "prepared"
	ref, err := s.putJSON(cp)
	must(t, err)
	must(t, WriteJSON(filepath.Join(s.Dir, "checkpoint.json"), ref))
	now := time.Now().UTC().Truncate(time.Second)
	plan, _ := testBeacon(t, now.Add(2*time.Hour))
	s.now = func() time.Time { return now }
	_, err = s.OpenCeremony(plan)
	must(t, err)
	i, err := s.InviteQueued("http://localhost", "alice", Core, 24*time.Hour)
	must(t, err)
	claim, err := s.Claim(i.Token, time.Hour)
	must(t, err)
	output := filepath.Join(t.TempDir(), "contribution")
	must(t, Contribute(s.Object(claim.Job.Input), output, claim.Job.Circuit.Shape))
	_, before, err := s.Lifecycle()
	must(t, err)
	s.now = func() time.Time { return plan.RoundTime }
	if _, err = s.InviteQueued("http://localhost", "bob", Core, time.Hour); err == nil {
		t.Fatal("invited after deadline")
	}
	if _, err = s.Claim(i.Token, time.Hour); err == nil {
		t.Fatal("reserved after deadline")
	}
	if _, err = s.Renew(i.Token, claim.Job.Circuit.Spec.ID, time.Hour); err == nil {
		t.Fatal("renewed after deadline")
	}
	if _, err = s.Accept(i.Token, claim.Job.Circuit.Spec.ID, output); err == nil {
		t.Fatal("accepted after deadline")
	}
	if _, err = s.CloseCeremony(); err == nil {
		t.Fatal("closed retroactively")
	}
	_, after, err := s.Lifecycle()
	must(t, err)
	if before != after {
		t.Fatal("rejected operations changed checkpoint")
	}
	var info map[string]any
	must(t, json.Unmarshal(plan.ChainInfo, &info))
	info["period"] = (uint64(1) << 63) + 1 // would wrap time.Duration back to one second
	raw, err := json.Marshal(info)
	must(t, err)
	if _, err = NewBeaconPlan(raw, plan.ChainHash, plan.Round); err == nil {
		t.Fatal("accepted overflowing period")
	}
}
