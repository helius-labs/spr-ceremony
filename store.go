package ceremony

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	mpc "github.com/consensys/gnark/backend/groth16/bn254/mpcsetup"
)

type SRSInfo struct {
	Format            string   `json:"format"`
	Engine            string   `json:"engine"`
	Artifact          Artifact `json:"artifact"`
	Provenance        string   `json:"provenance"`
	InsecureTestSetup bool     `json:"insecure_test_setup"`
}
type Circuit struct {
	Spec        Spec     `json:"spec"`
	R1CS        Artifact `json:"r1cs"`
	SRS         Artifact `json:"srs"`
	Initial     Artifact `json:"initial"`
	Constraints int      `json:"constraints"`
	Domain      uint64   `json:"domain"`
	Shape       Shape    `json:"shape"`
}
type Manifest struct {
	Format         string    `json:"format"`
	Engine         string    `json:"engine"`
	SourceRevision string    `json:"source_revision"`
	Phase1         SRSInfo   `json:"phase1"`
	Circuits       []Circuit `json:"circuits"`
}
type Receipt struct {
	Circuit     string    `json:"circuit"`
	Sequence    int       `json:"sequence"`
	Contributor string    `json:"contributor"`
	Parent      Artifact  `json:"parent"`
	Output      Artifact  `json:"output"`
	AcceptedAt  time.Time `json:"accepted_at"`
}
type State struct {
	Circuit string    `json:"circuit"`
	Head    Artifact  `json:"head"`
	History []Receipt `json:"history"`
}
type Job struct {
	Circuit  Circuit  `json:"circuit"`
	Input    Artifact `json:"input"`
	Sequence int      `json:"sequence"`
}
type Invitation struct {
	Format            string    `json:"format"`
	URL               string    `json:"url"`
	Token             string    `json:"token"`
	ManifestSHA256    string    `json:"manifest_sha256"`
	Contributor       string    `json:"contributor"`
	Track             string    `json:"track"`
	Expires           time.Time `json:"expires"`
	InsecureTestSetup bool      `json:"insecure_test_setup"`
	Jobs              []Job     `json:"jobs"`
	Mode              string    `json:"mode,omitempty"`
}
type Store struct {
	Dir              string
	Manifest         Manifest
	ManifestArtifact Artifact
	Remote           BlobStore
	now              func() time.Time
	checkpointRef    *Artifact
}
type Input struct {
	Spec Spec
	R1CS string
}

func (s *Store) Object(a Artifact) string   { return filepath.Join(s.Dir, "objects", a.SHA256) }
func (s *Store) statePath(id string) string { return filepath.Join(s.Dir, "state", id+".json") }
func (s *Store) State(id string) (State, error) {
	var st State
	cp, _, e := s.checkpoint()
	if e != nil {
		return st, e
	}
	ref, ok := cp.States[id]
	if !ok {
		return st, fmt.Errorf("unknown circuit")
	}
	if e = Check(s.Object(ref), ref); e != nil {
		return st, e
	}
	e = ReadJSON(s.Object(ref), &st)
	return st, e
}
func (s *Store) put(path string) (Artifact, error) {
	a, e := Digest(path)
	if e != nil {
		return a, e
	}
	dest := s.Object(a)
	if e = Check(dest, a); e == nil {
		return a, nil
	}
	e = Atomic(dest, func(w io.Writer) error {
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		defer f.Close()
		_, e = io.Copy(w, f)
		return e
	})
	return a, e
}
func (s *Store) putBinary(v io.WriterTo) (Artifact, error) {
	f, e := os.CreateTemp(s.Dir, ".binary-*")
	if e != nil {
		return Artifact{}, e
	}
	p := f.Name()
	f.Close()
	defer os.Remove(p)
	if e = WriteBinary(p, v); e != nil {
		return Artifact{}, e
	}
	return s.put(p)
}

// Prepare builds in a sibling staging directory and publishes only a complete
// manifest. It never replaces an existing ceremony directory.
func Prepare(dir, source, srsPath string, info SRSInfo, inputs []Input, maxDomain uint64) error {
	if len(inputs) == 0 || source == "" {
		return fmt.Errorf("source revision and nonempty circuit set are required")
	}
	if info.Format != Format || info.Engine != Engine || info.Provenance == "" {
		return fmt.Errorf("invalid SRS metadata")
	}
	if e := Check(srsPath, info.Artifact); e != nil {
		return e
	}
	if _, e := os.Stat(dir); !errors.Is(e, os.ErrNotExist) {
		return fmt.Errorf("output directory already exists or is inaccessible")
	}
	if e := os.MkdirAll(filepath.Dir(dir), 0700); e != nil {
		return e
	}
	tmp, e := os.MkdirTemp(filepath.Dir(dir), ".prepare-*")
	if e != nil {
		return e
	}
	defer os.RemoveAll(tmp)
	s := &Store{Dir: tmp, Manifest: Manifest{Format: Format, Engine: Engine, SourceRevision: source, Phase1: info}}
	srs, e := ReadSRS(srsPath, maxDomain)
	if e != nil {
		return e
	}
	if _, e = s.put(srsPath); e != nil {
		return e
	}
	seen := map[string]bool{}
	for _, in := range inputs {
		if !safeID.MatchString(in.Spec.ID) || seen[in.Spec.ID] {
			return fmt.Errorf("invalid or duplicate circuit ID")
		}
		seen[in.Spec.ID] = true
		if !info.InsecureTestSetup {
			if !IsCatalogSpec(in.Spec) {
				return fmt.Errorf("circuit is outside the agreed ceremony scope")
			}
			var compilation Compilation
			if e := ReadJSON(filepath.Join(filepath.Dir(in.R1CS), "compilation.json"), &compilation); e != nil {
				return fmt.Errorf("production preparation requires spr-zolana compilation metadata: %w", e)
			}
			if compilation.Engine != Engine || compilation.SourceRevision != source {
				return fmt.Errorf("compiled source revision or engine mismatch")
			}
			found := false
			for _, entry := range compilation.Circuits {
				if entry.Spec == in.Spec {
					if e := Check(in.R1CS, entry.R1CS); e != nil {
						return e
					}
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("circuit is absent from compilation metadata")
			}
		}
		c, e := ReadR1CS(in.R1CS)
		if e != nil {
			return fmt.Errorf("%s: %w", in.Spec.ID, e)
		}
		n := Domain(c)
		if n > maxDomain || n > uint64(len(srs.G2.Tau)) {
			return fmt.Errorf("%s: circuit domain %d exceeds supplied SRS or limit %d", in.Spec.ID, n, maxDomain)
		}
		trim := *srs
		trim.G1.Tau = srs.G1.Tau[:2*n-1]
		trim.G1.AlphaTau = srs.G1.AlphaTau[:n]
		trim.G1.BetaTau = srs.G1.BetaTau[:n]
		trim.G2.Tau = srs.G2.Tau[:n]
		p := new(mpc.Phase2)
		p.Initialize(c, &trim)
		ca, e := s.put(in.R1CS)
		if e != nil {
			return e
		}
		sa, e := s.putBinary(&trim)
		if e != nil {
			return e
		}
		pa, e := s.putBinary(p)
		if e != nil {
			return e
		}
		s.Manifest.Circuits = append(s.Manifest.Circuits, Circuit{Spec: in.Spec, R1CS: ca, SRS: sa, Initial: pa, Constraints: c.GetNbConstraints(), Domain: n, Shape: shapeOf(p)})
		if e = WriteJSON(s.statePath(in.Spec.ID), State{Circuit: in.Spec.ID, Head: pa, History: []Receipt{}}); e != nil {
			return e
		}
	}
	if e = WriteJSON(filepath.Join(tmp, "manifest.json"), s.Manifest); e != nil {
		return e
	}
	s.ManifestArtifact, e = Digest(filepath.Join(tmp, "manifest.json"))
	if e != nil {
		return e
	}
	if e = s.initializeCheckpoint(); e != nil {
		return e
	}
	return os.Rename(tmp, dir)
}

func Open(dir string) (*Store, error) {
	s := &Store{Dir: dir}
	p := filepath.Join(dir, "manifest.json")
	if e := ReadJSON(p, &s.Manifest); e != nil {
		return nil, e
	}
	a, e := Digest(p)
	if e != nil {
		return nil, e
	}
	s.ManifestArtifact = a
	if s.Manifest.Format != Format || s.Manifest.Engine != Engine || len(s.Manifest.Circuits) == 0 {
		return nil, fmt.Errorf("unsupported or empty manifest")
	}
	seen := map[string]bool{}
	for _, c := range s.Manifest.Circuits {
		if !safeID.MatchString(c.Spec.ID) || seen[c.Spec.ID] {
			return nil, fmt.Errorf("invalid circuit ID")
		}
		seen[c.Spec.ID] = true
	}
	if _, _, e := s.checkpoint(); e != nil {
		return nil, fmt.Errorf("checkpoint unavailable; restore or recreate a pre-checkpoint prototype directory: %w", e)
	}
	return s, nil
}
func (s *Store) circuit(id string) (Circuit, error) {
	for _, c := range s.Manifest.Circuits {
		if c.Spec.ID == id {
			return c, nil
		}
	}
	return Circuit{}, fmt.Errorf("unknown circuit")
}

func (s *Store) Invite(url, name, track string, ttl time.Duration) (Invitation, error) {
	return s.invite(url, name, track, ttl, "")
}

func (s *Store) InviteQueued(url, name, track string, ttl time.Duration) (Invitation, error) {
	return s.invite(url, name, track, ttl, "queue")
}

func (s *Store) invite(url, name, track string, ttl time.Duration, mode string) (Invitation, error) {
	if !safeID.MatchString(name) || (track != Core && track != Large) || ttl <= 0 {
		return Invitation{}, fmt.Errorf("invalid contributor, track, or lifetime")
	}
	unlock, e := s.lock()
	if e != nil {
		return Invitation{}, e
	}
	defer unlock()
	cp, _, e := s.checkpoint()
	if e != nil {
		return Invitation{}, e
	}
	if cp.Lifecycle.Phase != "open" {
		return Invitation{}, fmt.Errorf("ceremony is not open")
	}
	if cp.Lifecycle.Beacon != nil && !s.nowUTC().Before(cp.Lifecycle.Beacon.RoundTime) {
		return Invitation{}, fmt.Errorf("announced beacon deadline has passed")
	}
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return Invitation{}, e
	}
	i := Invitation{Format: Format, URL: url, Token: hex.EncodeToString(b), ManifestSHA256: s.ManifestArtifact.SHA256, Contributor: name, Track: track, Expires: time.Now().UTC().Add(ttl), InsecureTestSetup: s.Manifest.Phase1.InsecureTestSetup}
	i.Mode = mode
	for _, c := range s.Manifest.Circuits {
		if c.Spec.Track == track {
			st, e := s.State(c.Spec.ID)
			if e != nil {
				return i, e
			}
			i.Jobs = append(i.Jobs, Job{Circuit: c, Input: st.Head, Sequence: len(st.History) + 1})
		}
	}
	if len(i.Jobs) == 0 {
		return i, fmt.Errorf("no circuits in requested track")
	}
	return i, WriteJSON(filepath.Join(s.Dir, "invitations", hashBytes([]byte(i.Token))+".json"), i)
}

func (s *Store) invitation(token string) (Invitation, error) {
	var i Invitation
	if len(token) != 64 {
		return i, fmt.Errorf("invalid invitation")
	}
	e := ReadJSON(filepath.Join(s.Dir, "invitations", hashBytes([]byte(token))+".json"), &i)
	if e != nil || time.Now().After(i.Expires) {
		return i, fmt.Errorf("invalid or expired invitation")
	}
	return i, nil
}

// Accept serializes the commit under a coordinator lock, including across
// processes. S3 publication is part of acceptance when remote storage is enabled.
func (s *Store) Accept(token, id, path string) (Receipt, error) {
	i, e := s.invitation(token)
	if e != nil {
		return Receipt{}, e
	}
	var job *Job
	for j := range i.Jobs {
		if i.Jobs[j].Circuit.Spec.ID == id {
			job = &i.Jobs[j]
			break
		}
	}
	if job == nil {
		return Receipt{}, fmt.Errorf("circuit is not in invitation")
	}
	unlock, e := s.lock()
	if e != nil {
		return Receipt{}, e
	}
	defer unlock()
	cp, checkpoint, e := s.checkpoint()
	if e != nil {
		return Receipt{}, e
	}
	st, e := s.State(id)
	if e != nil {
		return Receipt{}, e
	}
	a, e := Digest(path)
	if e != nil {
		return Receipt{}, e
	}
	for _, r := range st.History {
		if r.Output == a && (r.Parent == job.Input || i.Mode == "queue") && r.Contributor == i.Contributor {
			return r, nil
		}
	}
	if cp.Lifecycle.Phase != "open" {
		return Receipt{}, fmt.Errorf("ceremony is not open")
	}
	if cp.Lifecycle.Beacon != nil && !s.nowUTC().Before(cp.Lifecycle.Beacon.RoundTime) {
		return Receipt{}, fmt.Errorf("announced beacon deadline has passed")
	}
	if i.Mode == "queue" {
		l, e := s.lease(token, id)
		if e != nil {
			return Receipt{}, e
		}
		job = &l.Job
	}
	if st.Head != job.Input || len(st.History)+1 != job.Sequence {
		return Receipt{}, fmt.Errorf("stale invitation: circuit has advanced; request a new invitation")
	}
	if e = Check(s.Object(st.Head), st.Head); e != nil {
		return Receipt{}, e
	}
	if e = Verify(s.Object(st.Head), path, job.Circuit.Shape); e != nil {
		return Receipt{}, fmt.Errorf("invalid contribution: %w", e)
	}
	a, e = s.put(path)
	if e != nil {
		return Receipt{}, e
	}
	if cp.Lifecycle.Beacon != nil && !s.nowUTC().Before(cp.Lifecycle.Beacon.RoundTime) {
		return Receipt{}, fmt.Errorf("announced beacon deadline passed during verification")
	}
	r := Receipt{Circuit: id, Sequence: job.Sequence, Contributor: i.Contributor, Parent: st.Head, Output: a, AcceptedAt: time.Now().UTC()}
	st.Head = a
	st.History = append(st.History, r)
	ref, e := s.putJSON(st)
	if e != nil {
		return Receipt{}, e
	}
	cp.States[id] = ref
	if e = s.commitCheckpoint(cp, checkpoint); e != nil {
		return Receipt{}, e
	}
	if i.Mode == "queue" {
		_ = os.Remove(s.leasePath(id))
	}
	return r, nil
}

// Audit reconstructs the initial state from pinned R1CS/SRS and replays every
// accepted transition. The manifest digest must be authenticated out of band.
func (s *Store) Audit(track string, maxDomain uint64) error {
	view, err := s.snapshot()
	if err != nil {
		return err
	}
	return view.audit(track, maxDomain, nil)
}

// audit can consume the reconstructed evaluations after each circuit's full
// transcript has verified. Export uses this to avoid repeating initialization.
func (s *Store) audit(track string, maxDomain uint64, consume func(Circuit, *mpc.SrsCommons, *mpc.Phase2Evaluations, State) error) error {
	if err := s.AuditCheckpoints(); err != nil {
		return err
	}
	count := 0
	for _, c := range s.Manifest.Circuits {
		if c.Spec.Track != track {
			continue
		}
		count++
		for _, a := range []Artifact{c.R1CS, c.SRS, c.Initial} {
			if e := Check(s.Object(a), a); e != nil {
				return e
			}
		}
		r1cs, e := ReadR1CS(s.Object(c.R1CS))
		if e != nil {
			return e
		}
		srs, e := ReadSRS(s.Object(c.SRS), maxDomain)
		if e != nil {
			return e
		}
		p := new(mpc.Phase2)
		evals := p.Initialize(r1cs, srs)
		if !reflect.DeepEqual(shapeOf(p), c.Shape) || Domain(r1cs) != c.Domain || r1cs.GetNbConstraints() != c.Constraints {
			return fmt.Errorf("circuit metadata mismatch")
		}
		h := hashWriter{}
		if _, e = p.WriteTo(&h); e != nil {
			return e
		}
		if h.artifact() != c.Initial {
			return fmt.Errorf("initialization mismatch: %s", c.Spec.ID)
		}
		st, e := s.State(c.Spec.ID)
		if e != nil {
			return e
		}
		parent := c.Initial
		for j, r := range st.History {
			if r.Circuit != c.Spec.ID || r.Sequence != j+1 || r.Parent != parent {
				return fmt.Errorf("broken transcript: %s", c.Spec.ID)
			}
			if e = Check(s.Object(r.Output), r.Output); e != nil {
				return e
			}
			if e = Verify(s.Object(parent), s.Object(r.Output), c.Shape); e != nil {
				return e
			}
			parent = r.Output
		}
		if st.Circuit != c.Spec.ID || st.Head != parent {
			return fmt.Errorf("head mismatch")
		}
		if consume != nil {
			if e = consume(c, srs, &evals, st); e != nil {
				return e
			}
		}
	}
	if count == 0 {
		return fmt.Errorf("no circuits in requested track")
	}
	return nil
}

// sameCircuit compares invitation contents with the pinned
// manifest rather than accepting client-selected vector lengths.
func sameCircuit(a, b Circuit) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
