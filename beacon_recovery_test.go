package ceremony

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/drand/drand/v2/common/chain"
	drandcrypto "github.com/drand/drand/v2/crypto"
	"github.com/drand/kyber/util/random"
)

type failedRecoveryPublication struct{ BlobStore }

func (failedRecoveryPublication) ConditionalPut(context.Context, string, Artifact, string) error {
	return fmt.Errorf("injected recovery publication outage")
}

func TestBeaconRecoveryPreservesContributionsAndFinalizes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "core")
	must(t, DemoCore(dir))
	s, err := Open(dir)
	must(t, err)
	cp, _, err := s.checkpoint()
	must(t, err)
	cp.Lifecycle = Lifecycle{Phase: "prepared"}
	ref, err := s.putJSON(cp)
	must(t, err)
	must(t, WriteJSON(filepath.Join(dir, "checkpoint.json"), ref))
	now := time.Now().UTC().Truncate(time.Second)
	s.now = func() time.Time { return now }
	scheme := drandcrypto.NewPedersenBLSUnchainedG1()
	secret := scheme.KeyGroup.Scalar().Pick(random.New())
	info := chain.Info{PublicKey: scheme.KeyGroup.Point().Mul(secret, nil), ID: "recovery-test-only", Period: time.Second, Scheme: scheme.Name, GenesisTime: now.Add(2*time.Hour).Unix() - 99, GenesisSeed: []byte("synthetic recovery test network")}
	raw, err := json.Marshal(info)
	must(t, err)
	plan, err := NewBeaconPlan(raw, info.HashString(), 100)
	must(t, err)
	proof := func(round uint64) BeaconProof {
		sig, err := scheme.AuthScheme.Sign(secret, scheme.DigestBeacon(signedBeacon{round: round}))
		must(t, err)
		return BeaconProof{Round: round, Signature: hex.EncodeToString(sig)}
	}
	_, err = s.OpenCeremony(plan)
	must(t, err)
	invitation, err := s.Invite("http://localhost", "alice", Core, 24*time.Hour)
	must(t, err)
	for _, job := range invitation.Jobs {
		output := filepath.Join(t.TempDir(), "contribution")
		must(t, Contribute(s.Object(job.Input), output, job.Circuit.Shape))
		_, err = s.Accept(invitation.Token, job.Circuit.Spec.ID, output)
		must(t, err)
	}
	before, original, err := s.checkpoint()
	must(t, err)
	futureRound := plan.Round + 7202
	if _, err = s.RecoverBeacon(futureRound, original.SHA256, "missed deadline"); err == nil {
		t.Fatal("changed an unexpired beacon plan")
	}
	now = plan.RoundTime.Add(time.Second)
	for _, attempt := range []struct {
		round       uint64
		pin, reason string
	}{
		{plan.Round, original.SHA256, "missed deadline"},
		{plan.Round + 3601, original.SHA256, "too soon"},
		{futureRound, strings.Repeat("f", 64), "stale checkpoint"},
		{futureRound, original.SHA256, ""},
		{futureRound, original.SHA256, "hidden\nrecord"},
	} {
		if _, err = s.RecoverBeacon(attempt.round, attempt.pin, attempt.reason); err == nil {
			t.Fatal("invalid recovery was accepted")
		}
	}
	recovered, err := s.RecoverBeacon(futureRound, original.SHA256, "closure deadline missed during operator outage")
	must(t, err)
	after, _, err := s.checkpoint()
	must(t, err)
	if !reflect.DeepEqual(before.States, after.States) || len(after.Lifecycle.Recoveries) != 1 || after.Lifecycle.Recoveries[0].PreviousCheckpoint != original || after.Previous == nil || *after.Previous != original {
		t.Fatal("recovery did not preserve and link the original state")
	}
	must(t, s.Audit(Core, 64))
	for _, mutate := range []func(*Checkpoint){
		func(c *Checkpoint) { c.Lifecycle.Recoveries[0].Reason = "" },
		func(c *Checkpoint) { c.Lifecycle.Recoveries[0].PreviousCheckpoint.SHA256 = strings.Repeat("e", 64) },
		func(c *Checkpoint) { c.Lifecycle.Recoveries[0].RecoveredAt = plan.RoundTime.Add(-time.Second) },
		func(c *Checkpoint) { c.Lifecycle.Recoveries[0].Next.Round++ },
		func(c *Checkpoint) { c.Lifecycle.Beacon.ChainHash = strings.Repeat("d", 64) },
		func(c *Checkpoint) { changed := c.Lifecycle.OpenedAt.Add(time.Second); c.Lifecycle.OpenedAt = &changed },
	} {
		broken, _, err := s.checkpoint()
		must(t, err)
		mutate(&broken)
		bad, err := s.putJSON(broken)
		must(t, err)
		must(t, WriteJSON(filepath.Join(dir, "checkpoint.json"), bad))
		if s.AuditCheckpoints() == nil {
			t.Fatal("auditor accepted a rewritten recovery")
		}
		must(t, WriteJSON(filepath.Join(dir, "checkpoint.json"), recovered))
	}
	// A failed S3 commit must leave the locally active plan unchanged.
	archive := testArchive{t.TempDir()}
	s.Remote = archive
	_, err = s.Sync(context.Background())
	must(t, err)
	now = after.Lifecycle.Beacon.RoundTime.Add(time.Second)
	s.Remote = failedRecoveryPublication{archive}
	if _, err = s.RecoverBeacon(futureRound+7202, recovered.SHA256, "second missed deadline"); err == nil {
		t.Fatal("recovery succeeded before publication")
	}
	_, stillCurrent, err := s.checkpoint()
	must(t, err)
	if stillCurrent != recovered {
		t.Fatal("failed recovery changed the local checkpoint")
	}
	remote, _, err := archive.ReadPointer(context.Background(), s.remoteKey("latest.json"))
	must(t, err)
	if remote != recovered {
		t.Fatal("failed recovery changed the published checkpoint")
	}
	s.Remote = archive
	// Close against the recovered plan, then use only that exact round's proof.
	now = after.Lifecycle.Beacon.RoundTime.Add(-time.Minute)
	_, err = s.CloseCeremony()
	must(t, err)
	_, closed, err := s.checkpoint()
	must(t, err)
	if _, err = s.RecoverBeacon(futureRound+7202, closed.SHA256, "replace a closed plan"); err == nil {
		t.Fatal("recovered a closed ceremony")
	}
	now = after.Lifecycle.Beacon.RoundTime.Add(time.Second)
	out := filepath.Join(t.TempDir(), "keys")
	if _, err = s.Finalize(out, proof(plan.Round), 64); err == nil {
		t.Fatal("used the superseded beacon")
	}
	_, err = s.Finalize(out, proof(futureRound), 64)
	must(t, err)
	must(t, s.VerifyExport(out, 64))
	must(t, s.Audit(Core, 64))
}
