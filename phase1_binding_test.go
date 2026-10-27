package ceremony

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditRejectsSubstitutedPhase1Source(t *testing.T) {
	s := demoStore(t)
	must(t, s.Audit(Core, 64))
	cp, _, err := s.checkpoint()
	must(t, err)
	other := filepath.Join(t.TempDir(), "unrelated.srs")
	must(t, TestSRS(other, 6))
	ref, err := s.put(other)
	must(t, err)
	// Keep valid circuit parameters and initializations but advertise an
	// unrelated full Phase 1 artifact. All hashes/checkpoints remain consistent.
	s.Manifest.Phase1.Artifact = ref
	manifestPath := filepath.Join(s.Dir, "manifest.json")
	must(t, WriteJSON(manifestPath, s.Manifest))
	s.ManifestArtifact, err = s.put(manifestPath)
	must(t, err)
	cp.Manifest = s.ManifestArtifact
	ref, err = s.putJSON(cp)
	must(t, err)
	must(t, WriteJSON(filepath.Join(s.Dir, "checkpoint.json"), ref))
	must(t, s.AuditCheckpoints())
	if err = s.Audit(Core, 64); err == nil || !strings.Contains(err.Error(), "does not derive") {
		t.Fatalf("substituted Phase 1 source was not rejected: %v", err)
	}
}

// Checks saved real-circuit rehearsals without repeating the expensive EC FFTs.
// Run after scripts/test-zolana.sh with SPR_CORE_REHEARSAL and SPR_TEST_SRS set.
func TestCoreRehearsalBindings(t *testing.T) {
	root, source := os.Getenv("SPR_CORE_REHEARSAL"), os.Getenv("SPR_TEST_SRS")
	if root == "" || source == "" {
		t.Skip("requires completed real core rehearsal and its marked test SRS")
	}
	var info SRSInfo
	must(t, ReadJSON(source+".json", &info))
	if !info.InsecureTestSetup || info.Format != Format || info.Engine != Engine {
		t.Fatal("expected marked rehearsal SRS")
	}
	must(t, Check(source, info.Artifact))
	full, err := ReadSRS(source, 1<<21)
	must(t, err)
	specs, err := Select(Core, "")
	must(t, err)
	for _, spec := range specs {
		t.Run(spec.ID, func(t *testing.T) {
			s, err := Open(filepath.Join(root, spec.ID, "ceremony"))
			must(t, err)
			if s.Manifest.Phase1 != info || len(s.Manifest.Circuits) != 1 || s.Manifest.Circuits[0].Spec != spec {
				t.Fatal("rehearsal provenance mismatch")
			}
			must(t, s.AuditCheckpoints())
			c := s.Manifest.Circuits[0]
			must(t, Check(s.Object(c.SRS), c.SRS))
			_, err = verifyDerivedSRS(full, c)
			must(t, err)
			st, err := s.State(spec.ID)
			must(t, err)
			if len(st.History) != 2 || st.History[0].Contributor != "alice" || st.History[1].Contributor != "bob" {
				t.Fatal("expected two completed rehearsal contributions")
			}
		})
	}
}
