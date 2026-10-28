package ceremony

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/helius-labs/spr-ceremony/internal/signing"
)

func signedStore(t *testing.T) *Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ceremony")
	must(t, DemoSigned(dir, false))
	s, err := Open(dir)
	must(t, err)
	return s
}

func TestSignedInvitationsReceiptsAndRecovery(t *testing.T) {
	s := signedStore(t)
	server := httptest.NewServer(s.Handler())
	defer server.Close()
	i, err := s.InviteQueued(server.URL, "alice", Core, time.Hour)
	must(t, err)
	if i.ID == i.Token || !validInvitationID(i.ID) || i.Signature == "" {
		t.Fatal("missing distinct signed identity")
	}
	if _, err = s.InviteQueued(server.URL, "alice", Core, time.Hour); err == nil {
		t.Fatal("duplicate active pseudonym accepted")
	}
	public, err := s.PublicSigningKey()
	must(t, err)
	must(t, verifySignedJSON(public, "invitation", i.ManifestSHA256, i))
	for _, change := range []func(*Invitation){
		func(x *Invitation) { x.ID = x.Token }, func(x *Invitation) { x.Token = x.ID },
		func(x *Invitation) { x.Contributor = "mallory" }, func(x *Invitation) { x.Expires = x.Expires.Add(time.Hour) },
		func(x *Invitation) { x.URL = "https://other.example" }, func(x *Invitation) { x.Track = Large },
		func(x *Invitation) { x.Signature = "" }, func(x *Invitation) { x.Mode = "" },
	} {
		altered := i
		change(&altered)
		if verifySignedJSON(public, "invitation", i.ManifestSHA256, altered) == nil {
			t.Fatal("altered invitation verified")
		}
	}
	c := NewClient(t.TempDir())
	c.Progress = io.Discard
	must(t, c.Run(context.Background(), i, Core))
	a, err := VerifyLocalContributions(c.Work, i)
	must(t, err)
	must(t, s.VerifyAttestation(a))
	must(t, s.Audit(Core, 64))
	for _, receipt := range a.Receipts {
		if receipt.Signature == "" || receipt.InvitationID != i.ID {
			t.Fatal("receipt is not signed for invitation")
		}
		altered := receipt
		altered.InvitationID = i.Token
		if s.verifyMessage("receipt", altered) == nil {
			t.Fatal("changed receipt identity verified")
		}
		if verifySignedJSON(public, "receipt", hashBytes([]byte("another ceremony")), receipt) == nil {
			t.Fatal("receipt replayed across ceremonies")
		}
	}
	// A rotation revokes the old bearer token and preserves progress by ID.
	reissued, err := s.ReissueInvitation(server.URL, i.ID, time.Hour)
	must(t, err)
	if reissued.ID != i.ID || reissued.Token == i.Token {
		t.Fatal("incorrect token rotation")
	}
	if _, err = s.Claim(i.Token, time.Minute); err == nil {
		t.Fatal("old bearer token survived rotation")
	}
	claim, err := s.Claim(reissued.Token, time.Minute)
	must(t, err)
	if !claim.Done || len(claim.Receipts) != 2 {
		t.Fatal("reissue lost progress")
	}
	must(t, c.Run(context.Background(), reissued, Core))

	archive := testArchive{dir: t.TempDir()}
	s.Remote = archive
	cp, err := s.Sync(context.Background())
	must(t, err)
	paths, err := filepath.Glob(filepath.Join(archive.dir, "objects", "*"))
	must(t, err)
	for _, path := range paths {
		b, e := os.ReadFile(path)
		must(t, e)
		if bytes.Contains(b, []byte("PGP PRIVATE KEY")) || bytes.Contains(b, []byte(i.Token)) || bytes.Contains(b, []byte(reissued.Token)) {
			t.Fatal("private material entered archive")
		}
	}
	dest := filepath.Join(t.TempDir(), "restored")
	must(t, Restore(context.Background(), archive, dest, s.ManifestArtifact, cp))
	restored, err := Open(dest)
	must(t, err)
	must(t, restored.Audit(Core, 64))
	if _, err = restored.ReissueInvitation(server.URL, i.ID, time.Hour); err == nil {
		t.Fatal("restored coordinator signed without key")
	}
	must(t, restored.LoadSigningKey(filepath.Join(s.Dir, "private", "coordinator.private.asc")))
	resumed, err := restored.ReissueInvitation(server.URL, i.ID, time.Hour)
	must(t, err)
	claim, err = restored.Claim(resumed.Token, time.Minute)
	must(t, err)
	if !claim.Done {
		t.Fatal("explicit ID recovery lost progress")
	}
	must(t, restored.RevokeInvitation(i.ID))
	fresh, err := restored.InviteQueued(server.URL, "alice", Core, time.Hour)
	must(t, err)
	if fresh.ID == i.ID {
		t.Fatal("new invitation reused identity")
	}
	claim, err = restored.Claim(fresh.Token, time.Minute)
	must(t, err)
	if claim.Done || claim.Job == nil {
		t.Fatal("pseudonym inherited earlier completion")
	}
}

func TestConcurrentPseudonymAndSignedAudit(t *testing.T) {
	s := signedStore(t)
	var wg sync.WaitGroup
	successes := make(chan Invitation, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			other, err := Open(s.Dir)
			if err == nil {
				if i, err := other.InviteQueued("https://ceremony.example", "same_name", Core, time.Hour); err == nil {
					successes <- i
				}
			}
		}()
	}
	wg.Wait()
	close(successes)
	if len(successes) != 1 {
		t.Fatal("duplicate active pseudonym race")
	}
	i := <-successes
	claim, err := s.Claim(i.Token, time.Minute)
	must(t, err)
	out := filepath.Join(t.TempDir(), "output")
	must(t, Contribute(s.Object(claim.Job.Input), out, claim.Job.Circuit.Shape))
	_, err = s.Accept(i.Token, claim.Job.Circuit.Spec.ID, out)
	must(t, err)
	cp, _, err := s.checkpoint()
	must(t, err)
	state, err := s.State(claim.Job.Circuit.Spec.ID)
	must(t, err)
	state.History[0].Contributor = "forged"
	cp.States[state.Circuit], err = s.putJSON(state)
	must(t, err)
	ref, err := s.putJSON(cp)
	must(t, err)
	must(t, WriteJSON(filepath.Join(s.Dir, "checkpoint.json"), ref))
	if s.AuditCheckpoints() == nil {
		t.Fatal("audit accepted unsigned identity rewrite")
	}
}

func TestBindKeyPreservesParametersAndRejectsOpenedHistory(t *testing.T) {
	s := demoStore(t)
	key, err := signing.New()
	must(t, err)
	out := filepath.Join(t.TempDir(), "bound")
	must(t, s.BindSigningKey(out, key.Public()))
	bound, err := Open(out)
	must(t, err)
	if bound.ManifestArtifact == s.ManifestArtifact || bound.Manifest.CoordinatorKey == nil {
		t.Fatal("manifest does not bind key")
	}
	b1, _ := json.Marshal(bound.Manifest.Circuits)
	b2, _ := json.Marshal(s.Manifest.Circuits)
	if !bytes.Equal(b1, b2) {
		t.Fatal("binding changed prepared parameters")
	}
	if _, err = bound.InviteQueued("https://ceremony.example", "alice", Core, time.Hour); err == nil {
		t.Fatal("bound store accepted missing private key")
	}
	private, err := key.Private()
	must(t, err)
	path := filepath.Join(t.TempDir(), "key.private.asc")
	must(t, os.WriteFile(path, private, 0644))
	if bound.LoadSigningKey(path) == nil {
		t.Fatal("accepted exposed private key permissions")
	}
	must(t, os.Chmod(path, 0600))
	must(t, bound.LoadSigningKey(path))
	other, err := signing.New()
	must(t, err)
	wrong, err := other.Private()
	must(t, err)
	must(t, os.WriteFile(path, wrong, 0600))
	if bound.LoadSigningKey(path) == nil {
		t.Fatal("accepted wrong private key")
	}
}
