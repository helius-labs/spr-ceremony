package ceremony

import (
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	mpc "github.com/consensys/gnark/backend/groth16/bn254/mpcsetup"
)

// VerifyExport independently reproduces keys for the recorded transcript prefix,
// including snapshots taken before additional contributions were accepted.
func (s *Store) VerifyExport(dir string, maxDomain uint64) error {
	view, err := s.snapshot()
	if err != nil {
		return err
	}
	return view.verifyExport(dir, maxDomain)
}

func (s *Store) verifyExport(dir string, maxDomain uint64) error {
	if err := validatePhase1Policy(s.Manifest.Phase1); err != nil {
		return err
	}
	var m ExportManifest
	if e := ReadJSON(filepath.Join(dir, "export.json"), &m); e != nil {
		return e
	}
	if m.Format != Format || m.Engine != Engine || m.ManifestSHA256 != s.ManifestArtifact.SHA256 || m.InsecureTestSetup != s.Manifest.Phase1.UsesTestParameters() {
		return fmt.Errorf("export provenance mismatch")
	}
	if !m.InsecureTestSetup || m.BeaconProof != nil {
		if m.BeaconPlan == nil || m.BeaconProof == nil || m.Closure == nil {
			return fmt.Errorf("final export requires a verified beacon and closure")
		}
		if err := validateBeaconPolicy(s.Manifest.Phase1.UsesTestParameters(), m.BeaconPlan); err != nil {
			return err
		}
		current, _, e := s.checkpoint()
		if e != nil {
			return e
		}
		if !samePlan(current.Lifecycle.Beacon, m.BeaconPlan) {
			return fmt.Errorf("export uses a different announced beacon")
		}
		included, e := s.containsCheckpoint(*m.Closure)
		if e != nil {
			return e
		}
		if !included {
			return fmt.Errorf("closure is not in the published checkpoint history")
		}
		b, e := VerifyBeacon(*m.BeaconPlan, *m.BeaconProof)
		if e != nil {
			return e
		}
		if hex.EncodeToString(b) != m.Beacon {
			return fmt.Errorf("export beacon mismatch")
		}
		if e = Check(s.Object(*m.Closure), *m.Closure); e != nil {
			return e
		}
		var closed Checkpoint
		if e = ReadJSON(s.Object(*m.Closure), &closed); e != nil {
			return e
		}
		if closed.Manifest != s.ManifestArtifact || closed.Lifecycle.Phase != "closed" || !samePlan(closed.Lifecycle.Beacon, m.BeaconPlan) || closed.Lifecycle.ClosedAt == nil || !closed.Lifecycle.ClosedAt.Before(m.BeaconPlan.RoundTime) {
			return fmt.Errorf("invalid ceremony closure")
		}
		for _, entry := range m.Circuits {
			if closed.Lifecycle.ClosedHeads[entry.Circuit.Spec.ID] != entry.Head {
				return fmt.Errorf("export differs from closed heads")
			}
		}
	}
	beacon, e := hex.DecodeString(m.Beacon)
	if e != nil || len(beacon) < 32 {
		return fmt.Errorf("invalid export beacon")
	}
	expected := 0
	for _, c := range s.Manifest.Circuits {
		if c.Spec.Track == m.Track {
			expected++
		}
	}
	if len(m.Circuits) != expected {
		return fmt.Errorf("incomplete export")
	}
	entries := map[string]ExportedCircuit{}
	for _, entry := range m.Circuits {
		c, e := s.circuit(entry.Circuit.Spec.ID)
		if e != nil {
			return e
		}
		if !sameCircuit(c, entry.Circuit) || c.Spec.Track != m.Track || entries[c.Spec.ID].Circuit.Spec.ID != "" {
			return fmt.Errorf("export circuit mismatch")
		}
		entries[c.Spec.ID] = entry
	}
	return s.audit(m.Track, maxDomain, func(c Circuit, srs *mpc.SrsCommons, evals *mpc.Phase2Evaluations, st State) error {
		entry, ok := entries[c.Spec.ID]
		if !ok {
			return fmt.Errorf("export omitted circuit")
		}
		n := entry.Contributions
		if n < 1 || n > len(st.History) || st.History[n-1].Output != entry.Head {
			return fmt.Errorf("export head is not an accepted transcript prefix")
		}
		p, e := ReadPhase2(s.Object(entry.Head), c.Shape)
		if e != nil {
			return e
		}
		pk, vk := p.Seal(srs, evals, beacon)
		for _, pair := range []struct {
			name  string
			value io.WriterTo
			ref   Artifact
		}{{".pk", pk, entry.PK}, {".vk", vk, entry.VK}} {
			h := hashWriter{}
			if _, e = pair.value.WriteTo(&h); e != nil {
				return e
			}
			if h.artifact() != pair.ref {
				return fmt.Errorf("exported key differs from ceremony transcript: %s%s", c.Spec.ID, pair.name)
			}
			if e = Check(filepath.Join(dir, c.Spec.ID+pair.name), pair.ref); e != nil {
				return e
			}
		}
		return nil
	})
}

type ExportedCircuit struct {
	Circuit       Circuit  `json:"circuit"`
	Head          Artifact `json:"head"`
	Contributions int      `json:"contributions"`
	PK            Artifact `json:"pk"`
	VK            Artifact `json:"vk"`
}
type ExportManifest struct {
	Format            string            `json:"format"`
	Engine            string            `json:"engine"`
	ManifestSHA256    string            `json:"manifest_sha256"`
	Track             string            `json:"track"`
	Beacon            string            `json:"beacon"`
	InsecureTestSetup bool              `json:"insecure_test_setup"`
	Circuits          []ExportedCircuit `json:"circuits"`
	BeaconPlan        *BeaconPlan       `json:"beacon_plan,omitempty"`
	BeaconProof       *BeaconProof      `json:"beacon_proof,omitempty"`
	Closure           *Artifact         `json:"closure,omitempty"`
}

// Export creates a reproducible key snapshot. It does not deploy keys or rotate
// any Zolana lockfile. Further contributions need a new snapshot and beacon.
func (s *Store) Export(track, out string, beacon []byte, maxDomain uint64) error {
	if !s.Manifest.Phase1.UsesTestParameters() {
		return fmt.Errorf("production keys require close and finalize with the announced beacon")
	}
	view, err := s.snapshot()
	if err != nil {
		return err
	}
	return view.export(track, out, beacon, maxDomain, nil)
}

func (s *Store) export(track, out string, beacon []byte, maxDomain uint64, proof *BeaconProof) error {
	if len(beacon) < 32 {
		return fmt.Errorf("beacon must contain at least 32 bytes")
	}
	if _, e := os.Stat(out); !os.IsNotExist(e) {
		return fmt.Errorf("export directory already exists or is inaccessible")
	}
	if e := os.MkdirAll(filepath.Dir(out), 0700); e != nil {
		return e
	}
	tmp, e := os.MkdirTemp(filepath.Dir(out), ".export-*")
	if e != nil {
		return e
	}
	defer os.RemoveAll(tmp)
	m := ExportManifest{Format: Format, Engine: Engine, ManifestSHA256: s.ManifestArtifact.SHA256, Track: track, Beacon: fmt.Sprintf("%x", beacon), InsecureTestSetup: s.Manifest.Phase1.UsesTestParameters()}
	if proof != nil {
		cp, ref, e := s.checkpoint()
		if e != nil {
			return e
		}
		m.BeaconPlan = cp.Lifecycle.Beacon
		m.BeaconProof = proof
		m.Closure = &ref
	}
	e = s.audit(track, maxDomain, func(c Circuit, srs *mpc.SrsCommons, evals *mpc.Phase2Evaluations, st State) error {
		if len(st.History) == 0 {
			return fmt.Errorf("%s has no accepted contributions", c.Spec.ID)
		}
		p, e := ReadPhase2(s.Object(st.Head), c.Shape)
		if e != nil {
			return e
		}
		pk, vk := p.Seal(srs, evals, beacon)
		pkPath := filepath.Join(tmp, c.Spec.ID+".pk")
		vkPath := filepath.Join(tmp, c.Spec.ID+".vk")
		if e = WriteBinary(pkPath, pk); e != nil {
			return e
		}
		if e = WriteBinary(vkPath, vk); e != nil {
			return e
		}
		pa, e := Digest(pkPath)
		if e != nil {
			return e
		}
		va, e := Digest(vkPath)
		if e != nil {
			return e
		}
		m.Circuits = append(m.Circuits, ExportedCircuit{Circuit: c, Head: st.Head, Contributions: len(st.History), PK: pa, VK: va})
		return nil
	})
	if e != nil {
		return e
	}
	if e = WriteJSON(filepath.Join(tmp, "export.json"), m); e != nil {
		return e
	}
	return os.Rename(tmp, out)
}
