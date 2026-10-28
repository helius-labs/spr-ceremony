package ceremony

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProductionPhase1PolicyAtEveryEntryPoint(t *testing.T) {
	s := demoStore(t)
	cp, _, err := s.checkpoint()
	must(t, err)
	s.Manifest.Phase1.InsecureTestSetup = false
	// Rebind an internally consistent manifest/checkpoint to arbitrary test SRS
	// bytes. Production must reject the trust root, even though its hash matches.
	must(t, WriteJSON(filepath.Join(s.Dir, "manifest.json"), s.Manifest))
	s.ManifestArtifact, err = s.put(filepath.Join(s.Dir, "manifest.json"))
	must(t, err)
	cp.Manifest = s.ManifestArtifact
	cp.Lifecycle = Lifecycle{Phase: "prepared"}
	ref, err := s.putJSON(cp)
	must(t, err)
	must(t, WriteJSON(filepath.Join(s.Dir, "checkpoint.json"), ref))
	plan, _ := testBeacon(t, time.Now().Add(2*time.Hour))
	c := s.Manifest.Circuits[0]
	checks := map[string]func() error{
		"prepare": func() error {
			return Prepare(filepath.Join(t.TempDir(), "prepared"), "reviewed-source", s.Object(s.Manifest.Phase1.Artifact), s.Manifest.Phase1, []Input{{Spec: c.Spec, R1CS: s.Object(c.R1CS)}}, 64)
		},
		"open":          func() error { _, err := s.OpenCeremony(plan); return err },
		"audit":         func() error { return s.Audit(Core, 64) },
		"verify-export": func() error { return s.VerifyExport(t.TempDir(), 64) },
	}
	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			if err := check(); err == nil || !strings.Contains(err.Error(), "pinned public power-21 SRS") {
				t.Fatalf("arbitrary production trust root was not rejected: %v", err)
			}
		})
	}
	info := s.Manifest.Phase1
	info.Artifact = publicPower21().SRS
	must(t, validatePhase1Policy(info))
	info.Artifact.Size++
	if validatePhase1Policy(info) == nil {
		t.Fatal("production SRS size was not pinned")
	}
}

func TestProductionRequiresQuicknet(t *testing.T) {
	synthetic, proof := testBeacon(t, time.Now().Add(2*time.Hour))
	_, err := VerifyBeacon(synthetic, proof)
	must(t, err)
	if validateBeaconPolicy(false, &synthetic) == nil {
		t.Fatal("self-issued beacon key passed the production trust policy")
	}
	must(t, validateBeaconPolicy(true, &synthetic))
	s := demoStore(t)
	s.Manifest.Phase1.InsecureTestSetup = false
	s.Manifest.Phase1.Artifact = publicPower21().SRS
	if _, err = s.OpenCeremony(synthetic); err == nil || !strings.Contains(err.Error(), "pinned quicknet") {
		t.Fatalf("opening accepted a different beacon network: %v", err)
	}
	cp, _, err := s.checkpoint()
	must(t, err)
	must(t, WriteJSON(filepath.Join(s.Dir, "manifest.json"), s.Manifest))
	s.ManifestArtifact, err = s.put(filepath.Join(s.Dir, "manifest.json"))
	must(t, err)
	cp.Manifest = s.ManifestArtifact
	cp.Lifecycle.Beacon = &synthetic
	ref, err := s.putJSON(cp)
	must(t, err)
	must(t, WriteJSON(filepath.Join(s.Dir, "checkpoint.json"), ref))
	if err = s.AuditCheckpoints(); err == nil || !strings.Contains(err.Error(), "pinned quicknet") {
		t.Fatalf("audit accepted another production network: %v", err)
	}
	exportDir := t.TempDir()
	metadata := ExportManifest{Format: Format, Engine: Engine, ManifestSHA256: s.ManifestArtifact.SHA256, BeaconPlan: &synthetic, BeaconProof: &proof, Closure: &ref}
	must(t, WriteJSON(filepath.Join(exportDir, "export.json"), metadata))
	if err = s.VerifyExport(exportDir, 64); err == nil || !strings.Contains(err.Error(), "pinned quicknet") {
		t.Fatalf("export accepted another production network: %v", err)
	}
	raw, err := os.ReadFile("docs/evidence/live-beacon.json")
	must(t, err)
	var fixture struct {
		ChainInfo json.RawMessage `json:"chain_info"`
		Proof     BeaconProof     `json:"proof"`
	}
	must(t, json.Unmarshal(raw, &fixture))
	quicknet, err := NewBeaconPlan(fixture.ChainInfo, QuicknetChainHash, fixture.Proof.Round)
	must(t, err)
	must(t, validateBeaconPolicy(false, &quicknet))
	_, err = VerifyBeacon(quicknet, fixture.Proof)
	must(t, err)
}

func TestLegacyMarkerCannotDisablePublicParameterChecks(t *testing.T) {
	s := demoStore(t)
	s.Manifest.Phase1.Artifact = publicPower21().SRS
	if !s.Manifest.Phase1.InsecureTestSetup || s.Manifest.Phase1.UsesTestParameters() {
		t.Fatal("legacy marker changed public parameter classification")
	}
	plan, _ := testBeacon(t, time.Now().Add(2*time.Hour))
	if _, err := s.OpenCeremony(plan); err == nil || !strings.Contains(err.Error(), "pinned quicknet") {
		t.Fatal("legacy marker bypassed quicknet pin")
	}
	s.Manifest.CoordinatorKey = nil
	if s.RequireSigningKey() == nil {
		t.Fatal("legacy marker bypassed coordinator signatures")
	}
	if s.Export(Core, t.TempDir(), make([]byte, 32), 64) == nil {
		t.Fatal("legacy marker allowed unplanned export")
	}
}
