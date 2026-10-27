package ceremony

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type testArchive struct{ dir string }

func (a testArchive) PutObject(_ context.Context, key, path string, ref Artifact) error {
	if err := Check(path, ref); err != nil {
		return err
	}
	return Atomic(filepath.Join(a.dir, key), func(w io.Writer) error {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	})
}
func (a testArchive) GetObject(ctx context.Context, key, path string, ref Artifact) error {
	return (testArchive{dir: filepath.Dir(path)}).PutObject(ctx, filepath.Base(path), filepath.Join(a.dir, key), ref)
}
func (a testArchive) ReadPointer(_ context.Context, key string) (Artifact, string, error) {
	var ref Artifact
	err := ReadJSON(filepath.Join(a.dir, key), &ref)
	if os.IsNotExist(err) {
		return ref, "", ErrRemoteMissing
	}
	return ref, ref.SHA256, err
}
func (a testArchive) ConditionalPut(ctx context.Context, key string, ref Artifact, etag string) error {
	_, current, err := a.ReadPointer(ctx, key)
	if err != nil && err != ErrRemoteMissing {
		return err
	}
	if current != etag {
		return ErrRemoteConflict
	}
	return WriteJSON(filepath.Join(a.dir, key), ref)
}
func (a testArchive) DownloadURL(context.Context, string, time.Duration) (string, error) {
	return "", fmt.Errorf("not used by archive test")
}

func TestCheckpointDetectsHistoryAndPlanRewrite(t *testing.T) {
	for _, rewrite := range []string{"head", "plan"} {
		t.Run(rewrite, func(t *testing.T) {
			s := demoStore(t)
			i, err := s.Invite("http://localhost", "alice", Core, time.Hour)
			must(t, err)
			job := i.Jobs[0]
			output := filepath.Join(t.TempDir(), "output")
			must(t, Contribute(s.Object(job.Input), output, job.Circuit.Shape))
			_, err = s.Accept(i.Token, job.Circuit.Spec.ID, output)
			must(t, err)
			must(t, s.AuditCheckpoints())
			cp, ref, err := s.checkpoint()
			must(t, err)
			if rewrite == "head" {
				var initial Checkpoint
				must(t, ReadJSON(s.Object(*cp.Previous), &initial))
				cp.States[job.Circuit.Spec.ID] = initial.States[job.Circuit.Spec.ID]
			} else {
				plan, _ := testBeacon(t, time.Now().Add(2*time.Hour))
				cp.Lifecycle.Beacon = &plan
			}
			must(t, s.commitCheckpoint(cp, ref))
			if s.AuditCheckpoints() == nil {
				t.Fatal("accepted rewritten checkpoint")
			}
		})
	}
}
