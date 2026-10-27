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
	var m ExportManifest
	if e := ReadJSON(filepath.Join(dir, "export.json"), &m); e != nil {
		return e
	}
	if m.Format != Format || m.Engine != Engine || m.ManifestSHA256 != s.ManifestArtifact.SHA256 || m.InsecureTestSetup != s.Manifest.Phase1.InsecureTestSetup {
		return fmt.Errorf("export provenance mismatch")
	}
	beacon, e := hex.DecodeString(m.Beacon)
	if e != nil || len(beacon) < 32 {
		return fmt.Errorf("invalid export beacon")
	}
	if e = s.Audit(m.Track, maxDomain); e != nil {
		return e
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
	seen := map[string]bool{}
	for _, entry := range m.Circuits {
		c, e := s.circuit(entry.Circuit.Spec.ID)
		if e != nil {
			return e
		}
		if !sameCircuit(c, entry.Circuit) || c.Spec.Track != m.Track || seen[c.Spec.ID] {
			return fmt.Errorf("export circuit mismatch")
		}
		seen[c.Spec.ID] = true
		st, e := s.State(c.Spec.ID)
		if e != nil {
			return e
		}
		n := entry.Contributions
		if n < 1 || n > len(st.History) || st.History[n-1].Output != entry.Head {
			return fmt.Errorf("export head is not an accepted transcript prefix")
		}
		r1cs, e := ReadR1CS(s.Object(c.R1CS))
		if e != nil {
			return e
		}
		srs, e := ReadSRS(s.Object(c.SRS), maxDomain)
		if e != nil {
			return e
		}
		initial := new(mpc.Phase2)
		evals := initial.Initialize(r1cs, srs)
		p, e := ReadPhase2(s.Object(entry.Head), c.Shape)
		if e != nil {
			return e
		}
		pk, vk := p.Seal(srs, &evals, beacon)
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
	}
	return nil
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
}

// Export creates a reproducible key snapshot. It does not deploy keys or rotate
// any Zolana lockfile. Further contributions need a new snapshot and beacon.
func (s *Store) Export(track, out string, beacon []byte, maxDomain uint64) error {
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
	m := ExportManifest{Format: Format, Engine: Engine, ManifestSHA256: s.ManifestArtifact.SHA256, Track: track, Beacon: fmt.Sprintf("%x", beacon), InsecureTestSetup: s.Manifest.Phase1.InsecureTestSetup}
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
