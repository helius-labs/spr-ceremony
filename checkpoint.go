package ceremony

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// BlobStore stores public ceremony data only. ConditionalPut must implement an
// atomic compare-and-swap: empty etag means create only, never overwrite.
type BlobStore interface {
	PutObject(context.Context, string, string, Artifact) error
	GetObject(context.Context, string, string, Artifact) error
	ReadPointer(context.Context, string) (Artifact, string, error)
	ConditionalPut(context.Context, string, Artifact, string) error
	DownloadURL(context.Context, string, time.Duration) (string, error)
}

var ErrPublicationUnavailable = errors.New("remote checkpoint publication failed")

var ErrRemoteMissing = errors.New("remote checkpoint does not exist")
var ErrRemoteConflict = errors.New("remote checkpoint changed; restore the published checkpoint before continuing")

// Checkpoint is an immutable, hash-linked index. A single atomic pointer is the
// commit point for all circuit heads, lifecycle changes, and published keys.
// Invitation tokens and reservations are deliberately absent.
type Checkpoint struct {
	Format    string              `json:"format"`
	Manifest  Artifact            `json:"manifest"`
	Sequence  uint64              `json:"sequence"`
	Previous  *Artifact           `json:"previous,omitempty"`
	States    map[string]Artifact `json:"states"`
	Lifecycle Lifecycle           `json:"lifecycle"`
	Exports   []Artifact          `json:"exports,omitempty"`
}

type RecoveryPoint struct {
	Manifest   Artifact `json:"manifest"`
	Checkpoint Artifact `json:"checkpoint"`
}

func (s *Store) lock() (func(), error) {
	if s.checkpointRef != nil {
		return nil, fmt.Errorf("checkpoint snapshot is read-only")
	}
	f, err := os.OpenFile(filepath.Join(s.Dir, "coordinator.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

func (s *Store) putJSON(value any) (Artifact, error) {
	f, err := os.CreateTemp(s.Dir, ".json-*")
	if err != nil {
		return Artifact{}, err
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)
	if err = WriteJSON(path, value); err != nil {
		return Artifact{}, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return Artifact{}, err
	}
	if st.Size() > maxJSONBytes {
		return Artifact{}, fmt.Errorf("checkpoint metadata exceeds 4 MiB limit")
	}
	return s.put(path)
}

func (s *Store) checkpoint() (Checkpoint, Artifact, error) {
	var ref Artifact
	var cp Checkpoint
	if s.checkpointRef != nil {
		ref = *s.checkpointRef
	} else if err := ReadJSON(filepath.Join(s.Dir, "checkpoint.json"), &ref); err != nil {
		return cp, ref, err
	}
	if err := Check(s.Object(ref), ref); err != nil {
		return cp, ref, err
	}
	if err := ReadJSON(s.Object(ref), &cp); err != nil {
		return cp, ref, err
	}
	if cp.Format != Format || cp.Manifest != s.ManifestArtifact || len(cp.States) != len(s.Manifest.Circuits) {
		return cp, ref, fmt.Errorf("checkpoint manifest or circuit set mismatch")
	}
	for _, c := range s.Manifest.Circuits {
		a, ok := cp.States[c.Spec.ID]
		if !ok || a.Validate() != nil {
			return cp, ref, fmt.Errorf("checkpoint circuit missing: %s", c.Spec.ID)
		}
	}
	return cp, ref, nil
}

func (s *Store) initializeCheckpoint() error {
	cp := Checkpoint{Format: Format, Manifest: s.ManifestArtifact, States: map[string]Artifact{}, Lifecycle: Lifecycle{Phase: "prepared"}}
	if s.Manifest.Phase1.UsesTestParameters() {
		cp.Lifecycle.Phase = "open"
	}
	for _, c := range s.Manifest.Circuits {
		ref, err := s.put(s.statePath(c.Spec.ID))
		if err != nil {
			return err
		}
		cp.States[c.Spec.ID] = ref
	}
	if _, err := s.put(filepath.Join(s.Dir, "manifest.json")); err != nil {
		return err
	}
	ref, err := s.putJSON(cp)
	if err != nil {
		return err
	}
	if err = WriteJSON(filepath.Join(s.Dir, "checkpoint.json"), ref); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(s.Dir, "state"))
}

// commitCheckpoint is called under the coordinator lock. Remote publication
// precedes local acceptance; an S3 error can never produce a success receipt.
func (s *Store) commitCheckpoint(cp Checkpoint, parent Artifact) error {
	cp.Previous = &parent
	cp.Sequence++
	ref, err := s.putJSON(cp)
	if err != nil {
		return err
	}
	if s.Remote != nil {
		if err = s.publishCheckpoint(context.Background(), cp, ref, &parent); err != nil {
			return errors.Join(ErrPublicationUnavailable, err)
		}
	}
	if err = WriteJSON(filepath.Join(s.Dir, "checkpoint.json"), ref); err != nil {
		return fmt.Errorf("checkpoint publication requires recovery at %s: %w", ref.SHA256, err)
	}
	return nil
}

func (s *Store) remoteKey(suffix string) string {
	return "campaigns/" + s.ManifestArtifact.SHA256 + "/" + suffix
}
func objectKey(a Artifact) string { return "objects/" + a.SHA256 }

// references returns only authenticated public artifacts reachable from the
// checkpoint. Arbitrary files from the working directory are never uploaded.
func (s *Store) references(cp Checkpoint, ref Artifact) ([]Artifact, error) {
	refs := []Artifact{ref, cp.Manifest, s.Manifest.Phase1.Artifact}
	if s.Manifest.CoordinatorKey != nil {
		refs = append(refs, *s.Manifest.CoordinatorKey)
	}
	if cp.Previous != nil {
		refs = append(refs, *cp.Previous)
	}
	for _, c := range s.Manifest.Circuits {
		refs = append(refs, c.R1CS, c.SRS, c.Initial, cp.States[c.Spec.ID])
		var state State
		if err := Check(s.Object(cp.States[c.Spec.ID]), cp.States[c.Spec.ID]); err != nil {
			return nil, err
		}
		if err := ReadJSON(s.Object(cp.States[c.Spec.ID]), &state); err != nil {
			return nil, err
		}
		parent := c.Initial
		for n, receipt := range state.History {
			if receipt.Circuit != c.Spec.ID || receipt.Sequence != n+1 || receipt.Parent != parent || receipt.Output.Validate() != nil {
				return nil, fmt.Errorf("broken checkpoint history: %s", c.Spec.ID)
			}
			refs = append(refs, receipt.Output)
			parent = receipt.Output
		}
		if state.Circuit != c.Spec.ID || state.Head != parent {
			return nil, fmt.Errorf("invalid checkpoint head")
		}
	}
	for _, export := range cp.Exports {
		refs = append(refs, export)
		var metadata ExportManifest
		if err := Check(s.Object(export), export); err != nil {
			return nil, err
		}
		if err := ReadJSON(s.Object(export), &metadata); err != nil {
			return nil, err
		}
		if metadata.ManifestSHA256 != cp.Manifest.SHA256 {
			return nil, fmt.Errorf("export manifest mismatch")
		}
		for _, c := range metadata.Circuits {
			refs = append(refs, c.PK, c.VK)
		}
	}
	seen := map[string]bool{}
	var unique []Artifact
	for _, a := range refs {
		if err := a.Validate(); err != nil {
			return nil, err
		}
		if !seen[a.SHA256] {
			unique = append(unique, a)
			seen[a.SHA256] = true
		}
	}
	return unique, nil
}

func (s *Store) publishCheckpoint(ctx context.Context, cp Checkpoint, ref Artifact, parent *Artifact) error {
	key := s.remoteKey("latest.json")
	current, etag, err := s.Remote.ReadPointer(ctx, key)
	alreadyPublished := err == nil && current == ref
	if errors.Is(err, ErrRemoteMissing) {
		if parent != nil {
			return fmt.Errorf("remote is uninitialized; run sync first")
		}
		etag = ""
	} else if err != nil {
		return err
	} else if !alreadyPublished && (parent == nil || current != *parent) {
		return ErrRemoteConflict
	}
	known := map[string]bool{}
	if parent != nil && !alreadyPublished {
		var previous Checkpoint
		if err = ReadJSON(s.Object(*parent), &previous); err != nil {
			return err
		}
		oldRefs, e := s.references(previous, *parent)
		if e != nil {
			return e
		}
		for _, a := range oldRefs {
			known[a.SHA256] = true
		}
	}
	refs, err := s.references(cp, ref)
	if err != nil {
		return err
	}
	for _, a := range refs {
		if known[a.SHA256] {
			continue
		}
		if err = Check(s.Object(a), a); err != nil {
			return err
		}
		if err = s.Remote.PutObject(ctx, objectKey(a), s.Object(a), a); err != nil {
			return err
		}
	}
	if alreadyPublished {
		return nil
	}
	return s.Remote.ConditionalPut(ctx, key, ref, etag)
}

// Sync publishes a complete checkpoint, or confirms that the same checkpoint
// is already published. It refuses to overwrite a divergent remote history.
func (s *Store) Sync(ctx context.Context) (Artifact, error) {
	if s.Remote == nil {
		return Artifact{}, fmt.Errorf("S3 storage is not configured")
	}
	unlock, err := s.lock()
	if err != nil {
		return Artifact{}, err
	}
	defer unlock()
	cp, ref, err := s.checkpoint()
	if err != nil {
		return ref, err
	}
	if err = s.AuditCheckpoints(); err != nil {
		return ref, err
	}
	uploaded := map[string]bool{}
	putIndex := func(a Artifact) error {
		if uploaded[a.SHA256] {
			return nil
		}
		if err := Check(s.Object(a), a); err != nil {
			return err
		}
		if err := s.Remote.PutObject(ctx, objectKey(a), s.Object(a), a); err != nil {
			return err
		}
		uploaded[a.SHA256] = true
		return nil
	}
	// Include older checkpoint indexes when seeding a remote from local history.
	previous := cp.Previous
	for previous != nil {
		if err = putIndex(*previous); err != nil {
			return ref, err
		}
		var old Checkpoint
		if err = ReadJSON(s.Object(*previous), &old); err != nil {
			return ref, err
		}
		// Old state indexes are needed to independently inspect checkpoints.
		for _, state := range old.States {
			if err = putIndex(state); err != nil {
				return ref, err
			}
		}
		previous = old.Previous
	}
	return ref, s.publishCheckpoint(ctx, cp, ref, nil)
}

// Restore requires an independently authenticated checkpoint reference. It
// stages a new directory and verifies every downloaded byte before publishing.
func Restore(ctx context.Context, remote BlobStore, out string, manifest, checkpoint Artifact) error {
	if manifest.Validate() != nil || checkpoint.Validate() != nil {
		return fmt.Errorf("valid manifest and checkpoint references are required")
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		return fmt.Errorf("restore destination must be new")
	}
	if err := os.MkdirAll(filepath.Dir(out), 0700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(out), ".restore-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	s := &Store{Dir: tmp, ManifestArtifact: manifest}
	// Historical checkpoints share most artifacts. Authenticate each unique
	// object on arrival, then check the complete staged set before publication.
	// This map belongs only to this restore; it never trusts a previous run.
	verified := make(map[string]Artifact)
	fetch := func(a Artifact) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if a.Validate() != nil {
			return fmt.Errorf("invalid remote artifact")
		}
		if previous, ok := verified[a.SHA256]; ok {
			if previous != a {
				return fmt.Errorf("inconsistent remote artifact metadata")
			}
			return nil
		}
		if Check(s.Object(a), a) == nil {
			verified[a.SHA256] = a
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(s.Object(a)), 0700); err != nil {
			return err
		}
		if err := remote.GetObject(ctx, objectKey(a), s.Object(a), a); err != nil {
			return err
		}
		if err := Check(s.Object(a), a); err != nil {
			return err
		}
		verified[a.SHA256] = a
		return nil
	}
	if err = fetch(manifest); err != nil {
		return err
	}
	if err = ReadJSON(s.Object(manifest), &s.Manifest); err != nil {
		return err
	}
	if err = Atomic(filepath.Join(tmp, "manifest.json"), func(w io.Writer) error {
		f, e := os.Open(s.Object(manifest))
		if e != nil {
			return e
		}
		defer f.Close()
		_, e = io.Copy(w, f)
		return e
	}); err != nil {
		return err
	}
	current := checkpoint
	var newer *Checkpoint
	for {
		if err = fetch(current); err != nil {
			return err
		}
		var cp Checkpoint
		if err = ReadJSON(s.Object(current), &cp); err != nil {
			return err
		}
		if cp.Manifest != manifest || cp.Format != Format || len(cp.States) != len(s.Manifest.Circuits) {
			return fmt.Errorf("invalid remote checkpoint")
		}
		if newer != nil && (cp.Sequence >= newer.Sequence || cp.Sequence+1 != newer.Sequence) {
			return fmt.Errorf("broken checkpoint sequence")
		}
		for _, state := range cp.States {
			if err = fetch(state); err != nil {
				return err
			}
		}
		for _, exported := range cp.Exports {
			if err = fetch(exported); err != nil {
				return err
			}
		}
		refs, e := s.references(cp, current)
		if e != nil {
			return e
		}
		for _, a := range refs {
			if err = fetch(a); err != nil {
				return err
			}
		}
		if cp.Previous == nil {
			if cp.Sequence != 0 {
				return fmt.Errorf("checkpoint history is truncated")
			}
			break
		}
		newer = &cp
		current = *cp.Previous
	}
	if err = WriteJSON(filepath.Join(tmp, "checkpoint.json"), checkpoint); err != nil {
		return err
	}
	if _, err = Open(tmp); err != nil {
		return err
	}
	if err = s.AuditCheckpoints(); err != nil {
		return err
	}
	for _, a := range verified {
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = Check(s.Object(a), a); err != nil {
			return err
		}
	}
	return os.Rename(tmp, out)
}

// Snapshot creates a read-only view pinned to one immutable checkpoint. Audits
// must not mix live heads while another process accepts contributions.
func (s *Store) Snapshot(ref Artifact) (*Store, error) {
	view := *s
	view.checkpointRef = &ref
	if _, _, err := view.checkpoint(); err != nil {
		return nil, err
	}
	return &view, nil
}
func (s *Store) snapshot() (*Store, error) {
	_, ref, err := s.checkpoint()
	if err != nil {
		return nil, err
	}
	return s.Snapshot(ref)
}
