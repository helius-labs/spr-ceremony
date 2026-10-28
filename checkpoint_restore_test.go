package ceremony

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type restoreHook struct {
	BlobStore
	after func(Artifact, string) error
}

func (r restoreHook) GetObject(ctx context.Context, key, path string, ref Artifact) error {
	if err := r.BlobStore.GetObject(ctx, key, path, ref); err != nil {
		return err
	}
	return r.after(ref, path)
}

func TestRestoreChecksSharedArtifactsBeforePublication(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ceremony")
	must(t, DemoSigned(dir, false))
	s, err := Open(dir)
	must(t, err)
	i, err := s.InviteQueued("https://unused.example", "alice", Core, time.Hour)
	must(t, err)
	for range 2 {
		claim, err := s.Claim(i.Token, time.Minute)
		must(t, err)
		output := filepath.Join(t.TempDir(), "output")
		must(t, Contribute(s.Object(claim.Job.Input), output, claim.Job.Circuit.Shape))
		_, err = s.Accept(i.Token, claim.Job.Circuit.Spec.ID, output)
		must(t, err)
	}
	archive := testArchive{dir: t.TempDir()}
	s.Remote = archive
	checkpoint, err := s.Sync(context.Background())
	must(t, err)
	genesis := checkpoint
	for {
		var cp Checkpoint
		must(t, ReadJSON(s.Object(genesis), &cp))
		if cp.Previous == nil {
			break
		}
		genesis = *cp.Previous
	}
	for _, mode := range []string{"valid", "corrupt-download", "corrupt-after-verification"} {
		t.Run(mode, func(t *testing.T) {
			var initialPath string
			corrupted := false
			remote := restoreHook{BlobStore: archive, after: func(ref Artifact, path string) error {
				if ref == s.Manifest.Circuits[0].Initial {
					initialPath = path
				}
				if mode == "corrupt-download" && path == initialPath || mode == "corrupt-after-verification" && ref == genesis {
					if initialPath == "" {
						t.Fatal("fixture has not fetched the shared initial artifact")
					}
					corrupted = true
					return os.WriteFile(initialPath, []byte("corrupt staged artifact"), 0600)
				}
				return nil
			}}
			parent := t.TempDir()
			out := filepath.Join(parent, "restored")
			err := Restore(context.Background(), remote, out, s.ManifestArtifact, checkpoint)
			if mode == "valid" {
				must(t, err)
				restored, err := Open(out)
				must(t, err)
				must(t, restored.Audit(Core, 64))
				_, actual, err := restored.Lifecycle()
				must(t, err)
				if actual != checkpoint {
					t.Fatal("restore changed checkpoint")
				}
			} else if !corrupted || err == nil {
				t.Fatal("corrupted artifact was not rejected")
			} else if _, err := os.Stat(out); !os.IsNotExist(err) {
				t.Fatal("failed restore published an output directory")
			}
			staging, err := filepath.Glob(filepath.Join(parent, ".restore-*"))
			must(t, err)
			if len(staging) != 0 {
				t.Fatal("restore left a staging directory")
			}
		})
	}
}

func TestRestoreRejectsConflictingSizeAndCancellation(t *testing.T) {
	s := demoStore(t)
	archive := testArchive{dir: t.TempDir()}
	s.Remote = archive
	checkpoint, err := s.Sync(context.Background())
	must(t, err)
	conflicting := s.ManifestArtifact
	conflicting.Size++
	if err := Restore(context.Background(), archive, filepath.Join(t.TempDir(), "conflict"), s.ManifestArtifact, conflicting); err == nil {
		t.Fatal("conflicting size for the same digest was accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := Restore(ctx, archive, filepath.Join(t.TempDir(), "cancelled"), s.ManifestArtifact, checkpoint); !errors.Is(err, context.Canceled) {
		t.Fatalf("restore ignored cancellation: %v", err)
	}
}
