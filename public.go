package ceremony

import "fmt"

type PublicCircuit struct {
	ID            string    `json:"id"`
	R1CS          Artifact  `json:"r1cs"`
	Head          Artifact  `json:"head"`
	Contributions int       `json:"contributions"`
	PK            *Artifact `json:"pk,omitempty"`
	VK            *Artifact `json:"vk,omitempty"`
}

type PublicCeremony struct {
	Format            string          `json:"format"`
	Manifest          Artifact        `json:"manifest"`
	Checkpoint        Artifact        `json:"checkpoint"`
	Phase             string          `json:"phase"`
	InsecureTestSetup bool            `json:"insecure_test_setup"`
	Phase1            Artifact        `json:"phase1"`
	CoordinatorKey    *Artifact       `json:"coordinator_key,omitempty"`
	Participants      int             `json:"participants"`
	Circuits          []PublicCircuit `json:"circuits"`
	FinalExport       *Artifact       `json:"final_export,omitempty"`
}

// PublicView derives links from a single immutable checkpoint, never from
// invitations or temporary export files. Keys appear only in a finalized phase.
func (s *Store) PublicView() (PublicCeremony, error) {
	var view PublicCeremony
	cp, ref, err := s.checkpoint()
	if err != nil {
		return view, err
	}
	view = PublicCeremony{Format: "spr-public-ceremony-v1", Manifest: cp.Manifest, Checkpoint: ref, Phase: cp.Lifecycle.Phase, InsecureTestSetup: s.Manifest.Phase1.InsecureTestSetup, Phase1: s.Manifest.Phase1.Artifact, CoordinatorKey: s.Manifest.CoordinatorKey, Circuits: []PublicCircuit{}}
	keys := map[string]ExportedCircuit{}
	if cp.Lifecycle.Phase == "finalized" {
		if cp.Lifecycle.FinalExport == nil {
			return view, fmt.Errorf("missing final export")
		}
		view.FinalExport = cp.Lifecycle.FinalExport
		if err = Check(s.Object(*view.FinalExport), *view.FinalExport); err != nil {
			return view, err
		}
		var metadata ExportManifest
		if err = ReadJSON(s.Object(*view.FinalExport), &metadata); err != nil {
			return view, err
		}
		if metadata.ManifestSHA256 != cp.Manifest.SHA256 || metadata.InsecureTestSetup != view.InsecureTestSetup || metadata.Track != Core {
			return view, fmt.Errorf("final export mismatch")
		}
		for _, c := range metadata.Circuits {
			keys[c.Circuit.Spec.ID] = c
		}
	}
	names := map[string]bool{}
	for _, c := range s.Manifest.Circuits {
		if c.Spec.Track != Core {
			continue
		}
		stateRef := cp.States[c.Spec.ID]
		if err = Check(s.Object(stateRef), stateRef); err != nil {
			return view, err
		}
		var state State
		if err = ReadJSON(s.Object(stateRef), &state); err != nil {
			return view, err
		}
		item := PublicCircuit{ID: c.Spec.ID, R1CS: c.R1CS, Head: state.Head, Contributions: len(state.History)}
		for _, receipt := range state.History {
			names[receipt.InvitationID] = true
		}
		if view.FinalExport != nil {
			key, ok := keys[c.Spec.ID]
			if !ok || key.Head != state.Head || key.PK.Validate() != nil || key.VK.Validate() != nil {
				return view, fmt.Errorf("final circuit mismatch")
			}
			item.PK, item.VK = &key.PK, &key.VK
		}
		view.Circuits = append(view.Circuits, item)
	}
	view.Participants = len(names)
	return view, nil
}
