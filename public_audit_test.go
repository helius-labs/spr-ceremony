package ceremony

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestPublicAuditPinsHistoricalCheckpointWithoutCredentials(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ceremony")
	must(t, DemoSigned(dir, false))
	s, err := Open(dir)
	must(t, err)
	i, err := s.InviteQueued("https://unused.example", "alice", Core, time.Hour)
	must(t, err)
	contribute := func() {
		claim, err := s.Claim(i.Token, time.Minute)
		must(t, err)
		output := filepath.Join(t.TempDir(), "output.ph2")
		must(t, Contribute(s.Object(claim.Job.Input), output, claim.Job.Circuit.Shape))
		_, err = s.Accept(i.Token, claim.Job.Circuit.Spec.ID, output)
		must(t, err)
	}
	contribute()
	_, pinned, err := s.Lifecycle()
	must(t, err)
	contribute()
	_, latest, err := s.Lifecycle()
	must(t, err)
	if latest == pinned {
		t.Fatal("fixture did not advance")
	}
	var forbidden atomic.Bool
	var tickets atomic.Int64
	api := s.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" || strings.HasPrefix(r.URL.Path, "/admin") || r.URL.Path == "/v1/invitation" || r.URL.Path == "/v1/ceremony" || r.URL.Path == "/v1/checkpoint" {
			forbidden.Store(true)
			http.Error(w, "credentials or mutable head requested", http.StatusForbidden)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/downloads/") {
			tickets.Add(1)
		}
		api.ServeHTTP(w, r)
	}))
	defer server.Close()
	work := t.TempDir()
	options := AuditOptions{Workers: 2, Progress: io.Discard}
	point, snapshot, err := AuditPublic(context.Background(), server.URL, work, s.ManifestArtifact.SHA256, pinned.SHA256, 64, options)
	must(t, err)
	if point.Checkpoint != pinned || point.Manifest != s.ManifestArtifact || forbidden.Load() {
		t.Fatal("public audit used credentials or substituted the latest checkpoint")
	}
	view, err := Open(snapshot)
	must(t, err)
	accepted := 0
	for _, circuit := range view.Manifest.Circuits {
		state, err := view.State(circuit.Spec.ID)
		must(t, err)
		accepted += len(state.History)
	}
	if accepted != 1 {
		t.Fatal("historical snapshot contains later contributions")
	}
	downloads := tickets.Load()
	_, again, err := AuditPublic(context.Background(), server.URL, work, s.ManifestArtifact.SHA256, pinned.SHA256, 64, options)
	must(t, err)
	if again != snapshot || tickets.Load() != downloads {
		t.Fatal("cached audit redownloaded the transcript")
	}
	must(t, filepath.WalkDir(work, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Contains(data, []byte(i.Token)) || bytes.Contains(data, []byte("BEGIN PGP PRIVATE KEY")) {
				t.Fatal("private data entered the public audit workspace")
			}
		}
		return nil
	}))
	// A cached checkpoint pointer cannot silently replace the requested pin.
	must(t, WriteJSON(filepath.Join(snapshot, "checkpoint.json"), latest))
	if _, _, err = AuditPublic(context.Background(), server.URL, work, s.ManifestArtifact.SHA256, pinned.SHA256, 64, options); err == nil {
		t.Fatal("substituted cached checkpoint accepted")
	}
	must(t, WriteJSON(filepath.Join(snapshot, "checkpoint.json"), pinned))
	// A matching metadata pin must not bypass verification of cached artifacts.
	must(t, os.WriteFile(view.Object(view.Manifest.Circuits[0].Initial), []byte("corrupt cached initial key"), 0600))
	if _, _, err = AuditPublic(context.Background(), server.URL, work, s.ManifestArtifact.SHA256, pinned.SHA256, 64, options); err == nil {
		t.Fatal("corrupted cached artifact accepted")
	}
}

func TestPublicAuditRejectsCheckpointFromAnotherManifest(t *testing.T) {
	dir := t.TempDir()
	must(t, DemoSigned(filepath.Join(dir, "first"), false))
	must(t, DemoSigned(filepath.Join(dir, "second"), false))
	first, err := Open(filepath.Join(dir, "first"))
	must(t, err)
	second, err := Open(filepath.Join(dir, "second"))
	must(t, err)
	_, checkpoint, err := second.Lifecycle()
	must(t, err)
	// Both metadata objects have valid hashes but describe different ceremonies.
	body, err := os.ReadFile(second.Object(checkpoint))
	must(t, err)
	must(t, os.WriteFile(first.Object(checkpoint), body, 0600))
	server := httptest.NewServer(first.Handler())
	defer server.Close()
	_, _, err = AuditPublic(context.Background(), server.URL, t.TempDir(), first.ManifestArtifact.SHA256, checkpoint.SHA256, 64, AuditOptions{Workers: 1})
	if err == nil || !strings.Contains(err.Error(), "invalid remote checkpoint") {
		t.Fatalf("mismatched manifest and checkpoint not rejected: %v", err)
	}
}

func TestPublicAuditRejectsMissingPinsAndModifiedMetadata(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte("changed metadata"))
	}))
	defer server.Close()
	pin := strings.Repeat("a", 64)
	for _, args := range []struct {
		manifest, checkpoint string
		workers              int
	}{{"", pin, 1}, {pin, "", 1}, {"not-a-hash", pin, 1}, {pin, pin, 0}, {pin, pin, 42}} {
		if _, _, err := AuditPublic(context.Background(), server.URL, t.TempDir(), args.manifest, args.checkpoint, 64, AuditOptions{Workers: args.workers}); err == nil {
			t.Fatal("invalid pins or workers accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid arguments caused a network request")
	}
	if _, _, err := AuditPublic(context.Background(), server.URL, t.TempDir(), pin, pin, 64, AuditOptions{Workers: 1}); err == nil {
		t.Fatal("modified metadata accepted")
	}
}

func TestPublicMetadataBoundsAndRedirects(t *testing.T) {
	for _, mode := range []string{"oversized", "redirect"} {
		t.Run(mode, func(t *testing.T) {
			body := bytes.Repeat([]byte("x"), maxJSONBytes+1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if mode == "redirect" {
					http.Redirect(w, r, "/unexpected", http.StatusFound)
					return
				}
				_, _ = w.Write(body)
			}))
			defer server.Close()
			if _, err := pinnedPublicMetadata(context.Background(), NewClient(""), server.URL, hashBytes(body)); err == nil {
				t.Fatal("unsafe metadata response accepted")
			}
		})
	}
}

func TestPublicAndOperatorAuditShareWorkspaceLock(t *testing.T) {
	s, operator, origin := operatorFixture(t)
	_, checkpoint, err := s.Lifecycle()
	must(t, err)
	work := t.TempDir()
	lock, err := os.OpenFile(filepath.Join(work, ".operator-audit.lock"), os.O_CREATE|os.O_RDWR, 0600)
	must(t, err)
	defer lock.Close()
	must(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	_, _, err = AuditPublic(context.Background(), origin, work, s.ManifestArtifact.SHA256, checkpoint.SHA256, 64, AuditOptions{Workers: 1})
	if err == nil || !strings.Contains(err.Error(), "another audit") {
		t.Fatalf("public audit did not respect lock: %v", err)
	}
	_, _, err = operator.Audit(context.Background(), work, 64, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "another audit") {
		t.Fatalf("operator audit did not respect lock: %v", err)
	}
}
