package ceremony

import (
	"fmt"
	"reflect"
	"strings"
	"time"
	"unicode"
)

func validRecoveryReason(reason string) bool {
	return reason != "" && reason == strings.TrimSpace(reason) && len(reason) <= 512 && strings.IndexFunc(reason, unicode.IsControl) < 0
}

// RecoverBeacon appends a public transition without changing any circuit state.
// The caller must pin the checkpoint it reviewed. A closed/finalized ceremony,
// an unexpired plan, or another network cannot be substituted through recovery.
func (s *Store) RecoverBeacon(round uint64, expectedCheckpoint, reason string) (Artifact, error) {
	if !validRecoveryReason(reason) || (Artifact{SHA256: expectedCheckpoint, Size: 1}).Validate() != nil {
		return Artifact{}, fmt.Errorf("a checkpoint SHA256 and a public recovery reason are required")
	}
	unlock, err := s.lock()
	if err != nil {
		return Artifact{}, err
	}
	defer unlock()
	if err = s.AuditCheckpoints(); err != nil {
		return Artifact{}, err
	}
	cp, ref, err := s.checkpoint()
	if err != nil {
		return ref, err
	}
	if ref.SHA256 != expectedCheckpoint {
		return ref, fmt.Errorf("checkpoint changed; review it before recovering the beacon")
	}
	before, now := cp.Lifecycle.Beacon, s.nowUTC()
	if cp.Lifecycle.Phase != "open" || before == nil || now.Before(before.RoundTime) {
		return ref, fmt.Errorf("beacon recovery requires an open ceremony with a missed deadline")
	}
	if round <= before.Round {
		return ref, fmt.Errorf("recovery must use a later beacon round")
	}
	next, err := NewBeaconPlan(before.ChainInfo, before.ChainHash, round)
	if err != nil {
		return ref, err
	}
	if !now.Add(time.Hour).Before(next.RoundTime) {
		return ref, fmt.Errorf("recovery beacon must be more than one hour in the future")
	}
	if err = validateBeaconPolicy(s.Manifest.Phase1.UsesTestParameters(), &next); err != nil {
		return ref, err
	}
	if len(cp.Lifecycle.Recoveries) >= 16 {
		return ref, fmt.Errorf("beacon recovery history limit reached")
	}
	// Check every public object without changing a history, output, or head.
	refs, err := s.references(cp, ref)
	if err != nil {
		return ref, err
	}
	for _, artifact := range refs {
		if err = Check(s.Object(artifact), artifact); err != nil {
			return ref, err
		}
	}
	now = s.nowUTC()
	if !now.Add(time.Hour).Before(next.RoundTime) {
		return ref, fmt.Errorf("recovery beacon is no longer more than one hour in the future")
	}
	cp.Lifecycle.Recoveries = append(cp.Lifecycle.Recoveries, BeaconRecovery{PreviousCheckpoint: ref, Previous: *before, Next: next, RecoveredAt: now, Reason: reason})
	cp.Lifecycle.Beacon = &next
	if err = s.commitCheckpoint(cp, ref); err != nil {
		return Artifact{}, err
	}
	_, ref, err = s.checkpoint()
	return ref, err
}

func (s *Store) auditBeaconRecovery(previous, current Checkpoint) error {
	before, after := previous.Lifecycle, current.Lifecycle
	history := after.Recoveries
	if before.Phase != "open" || after.Phase != "open" || before.Beacon == nil || after.Beacon == nil || current.Previous == nil || len(history) != len(before.Recoveries)+1 || len(history) > 16 || (len(before.Recoveries) > 0 && !reflect.DeepEqual(before.Recoveries, history[:len(history)-1])) {
		return fmt.Errorf("invalid beacon recovery history")
	}
	recovery := history[len(history)-1]
	if recovery.PreviousCheckpoint != *current.Previous || !samePlan(before.Beacon, &recovery.Previous) || !samePlan(after.Beacon, &recovery.Next) || !validRecoveryReason(recovery.Reason) || recovery.RecoveredAt.Before(before.Beacon.RoundTime) || !recovery.RecoveredAt.Add(time.Hour).Before(after.Beacon.RoundTime) || after.Beacon.Round <= before.Beacon.Round || after.Beacon.ChainHash != before.Beacon.ChainHash {
		return fmt.Errorf("invalid beacon recovery plan or timing")
	}
	checked, err := NewBeaconPlan(before.Beacon.ChainInfo, before.Beacon.ChainHash, after.Beacon.Round)
	if err != nil || !samePlan(after.Beacon, &checked) {
		return fmt.Errorf("beacon recovery changed the network")
	}
	if err = validateBeaconPolicy(s.Manifest.Phase1.UsesTestParameters(), after.Beacon); err != nil {
		return err
	}
	after.Beacon, after.Recoveries = before.Beacon, before.Recoveries
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(previous.States, current.States) || !reflect.DeepEqual(previous.Exports, current.Exports) {
		return fmt.Errorf("beacon recovery changed ceremony data")
	}
	return nil
}
