package ceremony

import (
	"fmt"
	"os"
	"path/filepath"
)

// Attestation contains only public transcript references. It is not an identity
// signature or proof that the coordinator included these receipts in its final
// checkpoint; auditors compare them with the independently published transcript.
type Attestation struct {
	Format            string    `json:"format"`
	Engine            string    `json:"engine"`
	ManifestSHA256    string    `json:"manifest_sha256"`
	Contributor       string    `json:"contributor"`
	Track             string    `json:"track"`
	InsecureTestSetup bool      `json:"insecure_test_setup"`
	Receipts          []Receipt `json:"receipts"`
}

// VerifyLocalContributions works offline, using the authenticated invitation and
// retained parent/output files. Expired invitations remain usable for auditing.
// Every invited circuit must have a locally verified acceptance receipt.
func VerifyLocalContributions(work string, i Invitation) (Attestation, error) {
	a := Attestation{Format: Format, Engine: Engine, ManifestSHA256: i.ManifestSHA256,
		Contributor: i.Contributor, Track: i.Track, InsecureTestSetup: i.InsecureTestSetup}
	if i.Format != Format || !safeID.MatchString(i.Contributor) || len(i.Jobs) == 0 ||
		(Artifact{SHA256: i.ManifestSHA256, Size: 1}).Validate() != nil || (i.Track != Core && i.Track != Large) {
		return a, fmt.Errorf("invalid invitation for offline verification")
	}
	seen := map[string]bool{}
	for _, scope := range i.Jobs {
		id := scope.Circuit.Spec.ID
		if !safeID.MatchString(id) || seen[id] || scope.Circuit.Spec.Track != i.Track {
			return a, fmt.Errorf("invalid or duplicate circuit")
		}
		seen[id] = true
		dir := filepath.Join(work, i.ManifestSHA256, i.Contributor, id)
		entries, err := os.ReadDir(dir)
		if err != nil {
			return a, err
		}
		found := false
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			parentDir := filepath.Join(dir, entry.Name())
			var r Receipt
			if err = ReadJSON(filepath.Join(parentDir, "receipt.json"), &r); os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return a, err
			}
			if r.Circuit != id || r.Contributor != i.Contributor || r.Sequence < 1 || r.Parent.SHA256 != entry.Name() {
				return a, fmt.Errorf("local receipt mismatch: %s", id)
			}
			input, output := filepath.Join(parentDir, "input.ph2"), filepath.Join(parentDir, "output.ph2")
			if err = Check(input, r.Parent); err != nil {
				return a, err
			}
			if err = Check(output, r.Output); err != nil {
				return a, err
			}
			if err = Verify(input, output, scope.Circuit.Shape); err != nil {
				return a, err
			}
			a.Receipts = append(a.Receipts, r)
			found = true
		}
		if !found {
			return a, fmt.Errorf("no accepted local contribution for %s", id)
		}
	}
	return a, nil
}

// VerifyAttestation checks inclusion of every attested receipt and coverage of
// the full track in a fixed checkpoint. Audit separately verifies the MPC chain.
func (s *Store) VerifyAttestation(a Attestation) error {
	view, err := s.snapshot()
	if err != nil {
		return err
	}
	if a.Format != Format || a.Engine != Engine || a.ManifestSHA256 != view.ManifestArtifact.SHA256 ||
		a.InsecureTestSetup != view.Manifest.Phase1.InsecureTestSetup || !safeID.MatchString(a.Contributor) ||
		(a.Track != Core && a.Track != Large) || len(a.Receipts) == 0 {
		return fmt.Errorf("attestation provenance mismatch")
	}
	covered := map[string]bool{}
	seen := map[string]bool{}
	for _, r := range a.Receipts {
		c, err := view.circuit(r.Circuit)
		if err != nil {
			return err
		}
		key := fmt.Sprintf("%s/%d", r.Circuit, r.Sequence)
		if c.Spec.Track != a.Track || r.Contributor != a.Contributor || r.Sequence < 1 || seen[key] ||
			r.Parent.Validate() != nil || r.Output.Validate() != nil {
			return fmt.Errorf("invalid attested receipt")
		}
		st, err := view.State(r.Circuit)
		if err != nil {
			return err
		}
		if r.Sequence > len(st.History) {
			return fmt.Errorf("attested contribution is absent: %s #%d", r.Circuit, r.Sequence)
		}
		expected := st.History[r.Sequence-1]
		if !sameReceipt(r, expected) {
			return fmt.Errorf("attested contribution differs from checkpoint: %s #%d", r.Circuit, r.Sequence)
		}
		covered[r.Circuit], seen[key] = true, true
	}
	for _, c := range view.Manifest.Circuits {
		if c.Spec.Track == a.Track && !covered[c.Spec.ID] {
			return fmt.Errorf("attestation omits %s", c.Spec.ID)
		}
	}
	return nil
}
