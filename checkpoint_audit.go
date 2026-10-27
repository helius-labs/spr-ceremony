package ceremony

import (
	"fmt"
	"reflect"
	"slices"
)

// AuditCheckpoints validates the append-only publication history independently
// of MPC verification. It detects rolled-back heads and rewritten beacon plans.
func (s *Store) AuditCheckpoints() error {
	current, _, err := s.checkpoint()
	if err != nil {
		return err
	}
	for current.Previous != nil {
		if err = Check(s.Object(*current.Previous), *current.Previous); err != nil {
			return err
		}
		var previous Checkpoint
		if err = ReadJSON(s.Object(*current.Previous), &previous); err != nil {
			return err
		}
		if previous.Manifest != s.ManifestArtifact || previous.Format != Format || previous.Sequence >= current.Sequence || previous.Sequence+1 != current.Sequence || len(previous.States) != len(current.States) {
			return fmt.Errorf("broken checkpoint chain")
		}
		changes := 0
		for id, oldRef := range previous.States {
			newRef, ok := current.States[id]
			if !ok {
				return fmt.Errorf("checkpoint removed a circuit")
			}
			if oldRef == newRef {
				continue
			}
			changes++
			if err = Check(s.Object(oldRef), oldRef); err != nil {
				return err
			}
			if err = Check(s.Object(newRef), newRef); err != nil {
				return err
			}
			var oldState, newState State
			if err = ReadJSON(s.Object(oldRef), &oldState); err != nil {
				return err
			}
			if err = ReadJSON(s.Object(newRef), &newState); err != nil {
				return err
			}
			if oldState.Circuit != id || newState.Circuit != id || len(newState.History) != len(oldState.History)+1 || !reflect.DeepEqual(oldState.History, newState.History[:len(oldState.History)]) {
				return fmt.Errorf("checkpoint rewrote circuit history: %s", id)
			}
			last := newState.History[len(oldState.History)]
			if last.Circuit != id || last.Sequence != len(newState.History) || last.Parent != oldState.Head || last.Output != newState.Head {
				return fmt.Errorf("invalid checkpoint receipt")
			}
		}
		switch previous.Lifecycle.Phase + "->" + current.Lifecycle.Phase {
		case "open->open":
			if changes != 1 || !reflect.DeepEqual(previous.Lifecycle, current.Lifecycle) || !reflect.DeepEqual(previous.Exports, current.Exports) {
				return fmt.Errorf("invalid contribution checkpoint")
			}
		case "prepared->open":
			if changes != 0 || current.Lifecycle.ClosedAt != nil || len(current.Lifecycle.ClosedHeads) != 0 || current.Lifecycle.FinalExport != nil || current.Lifecycle.Beacon == nil || current.Lifecycle.OpenedAt == nil || !current.Lifecycle.OpenedAt.Before(current.Lifecycle.Beacon.RoundTime) || !reflect.DeepEqual(previous.Exports, current.Exports) {
				return fmt.Errorf("invalid opening checkpoint")
			}
			p := current.Lifecycle.Beacon
			checked, e := NewBeaconPlan(p.ChainInfo, p.ChainHash, p.Round)
			if e != nil || !checked.RoundTime.Equal(p.RoundTime) {
				return fmt.Errorf("invalid announced beacon plan")
			}
		case "open->closed":
			if changes != 0 || !reflect.DeepEqual(previous.Lifecycle.OpenedAt, current.Lifecycle.OpenedAt) || current.Lifecycle.FinalExport != nil || !samePlan(previous.Lifecycle.Beacon, current.Lifecycle.Beacon) || current.Lifecycle.ClosedAt == nil || !reflect.DeepEqual(previous.Exports, current.Exports) {
				return fmt.Errorf("invalid closure checkpoint")
			}
			if current.Lifecycle.Beacon != nil && !current.Lifecycle.ClosedAt.Before(current.Lifecycle.Beacon.RoundTime) {
				return fmt.Errorf("closure occurred after beacon")
			}
			if len(current.Lifecycle.ClosedHeads) != len(current.States) {
				return fmt.Errorf("closure omitted circuit heads")
			}
			for id, ref := range current.States {
				var st State
				if err = Check(s.Object(ref), ref); err != nil {
					return err
				}
				if err = ReadJSON(s.Object(ref), &st); err != nil {
					return err
				}
				if len(st.History) == 0 || st.Head != current.Lifecycle.ClosedHeads[id] {
					return fmt.Errorf("invalid closed head")
				}
			}
		case "closed->finalized":
			before, after := previous.Lifecycle, current.Lifecycle
			after.Phase = before.Phase
			after.FinalExport = nil
			if changes != 0 || !reflect.DeepEqual(before, after) || current.Lifecycle.FinalExport == nil || len(current.Exports) != len(previous.Exports)+1 || !slices.Equal(previous.Exports, current.Exports[:len(previous.Exports)]) || current.Exports[len(previous.Exports)] != *current.Lifecycle.FinalExport {
				return fmt.Errorf("invalid finalization checkpoint")
			}
		default:
			return fmt.Errorf("invalid lifecycle transition")
		}
		current = previous
	}
	if current.Lifecycle.OpenedAt != nil || current.Lifecycle.ClosedAt != nil || len(current.Lifecycle.ClosedHeads) != 0 || current.Sequence != 0 || len(current.Exports) != 0 || current.Lifecycle.Beacon != nil || current.Lifecycle.FinalExport != nil {
		return fmt.Errorf("invalid initial checkpoint")
	}
	if current.Lifecycle.Phase != "prepared" && !(s.Manifest.Phase1.InsecureTestSetup && current.Lifecycle.Phase == "open") {
		return fmt.Errorf("invalid initial ceremony phase")
	}
	for _, c := range s.Manifest.Circuits {
		ref, ok := current.States[c.Spec.ID]
		if !ok {
			return fmt.Errorf("initial checkpoint omitted circuit")
		}
		if err = Check(s.Object(ref), ref); err != nil {
			return err
		}
		var st State
		if err = ReadJSON(s.Object(ref), &st); err != nil {
			return err
		}
		if st.Circuit != c.Spec.ID || st.Head != c.Initial || len(st.History) != 0 {
			return fmt.Errorf("initial checkpoint contains contributions")
		}
	}
	return nil
}

func (s *Store) containsCheckpoint(target Artifact) (bool, error) {
	cp, ref, err := s.checkpoint()
	if err != nil {
		return false, err
	}
	for {
		if ref == target {
			return true, nil
		}
		if cp.Previous == nil {
			return false, nil
		}
		ref = *cp.Previous
		if err = Check(s.Object(ref), ref); err != nil {
			return false, err
		}
		var previous Checkpoint
		if err = ReadJSON(s.Object(ref), &previous); err != nil {
			return false, err
		}
		if previous.Sequence >= cp.Sequence {
			return false, fmt.Errorf("checkpoint sequence did not decrease")
		}
		cp = previous
	}
}
