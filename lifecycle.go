package ceremony

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Lifecycle struct {
	Phase       string              `json:"phase"`
	Beacon      *BeaconPlan         `json:"beacon,omitempty"`
	OpenedAt    *time.Time          `json:"opened_at,omitempty"`
	ClosedAt    *time.Time          `json:"closed_at,omitempty"`
	ClosedHeads map[string]Artifact `json:"closed_heads,omitempty"`
	FinalExport *Artifact           `json:"final_export,omitempty"`
	Recoveries  []BeaconRecovery    `json:"beacon_recoveries,omitempty"`
}

// BeaconRecovery preserves the superseded plan and its exact checkpoint.
// Recovery only resumes an open campaign after its closure deadline was missed.
type BeaconRecovery struct {
	PreviousCheckpoint Artifact   `json:"previous_checkpoint"`
	Previous           BeaconPlan `json:"previous"`
	Next               BeaconPlan `json:"next"`
	RecoveredAt        time.Time  `json:"recovered_at"`
	Reason             string     `json:"reason"`
}

type BeaconPlan struct {
	ChainInfo json.RawMessage `json:"chain_info"`
	ChainHash string          `json:"chain_hash"`
	Round     uint64          `json:"round"`
	RoundTime time.Time       `json:"round_time"`
}

func (s *Store) Lifecycle() (Lifecycle, Artifact, error) {
	cp, ref, err := s.checkpoint()
	return cp.Lifecycle, ref, err
}

func (s *Store) nowUTC() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func (s *Store) CloseCeremony() (Artifact, error) {
	unlock, err := s.lock()
	if err != nil {
		return Artifact{}, err
	}
	defer unlock()
	cp, ref, err := s.checkpoint()
	if err != nil {
		return ref, err
	}
	if cp.Lifecycle.Phase != "open" {
		return ref, fmt.Errorf("ceremony is not open")
	}
	now := s.nowUTC()
	if cp.Lifecycle.Beacon == nil && !s.Manifest.Phase1.UsesTestParameters() {
		return ref, fmt.Errorf("beacon plan is required")
	}
	if cp.Lifecycle.Beacon != nil && !now.Before(cp.Lifecycle.Beacon.RoundTime) {
		return ref, fmt.Errorf("planned beacon time has passed; cannot close retroactively")
	}
	leases, e := os.ReadDir(filepath.Join(s.Dir, "leases"))
	if e != nil && !os.IsNotExist(e) {
		return ref, e
	}
	for _, entry := range leases {
		if entry.IsDir() {
			continue
		}
		var lease Lease
		if e = ReadJSON(filepath.Join(s.Dir, "leases", entry.Name()), &lease); e != nil {
			return ref, e
		}
		if now.Before(lease.Expires) {
			return ref, fmt.Errorf("active reservation for %s; wait for completion or expiry", lease.Job.Circuit.Spec.ID)
		}
	}
	cp.Lifecycle.ClosedHeads = map[string]Artifact{}
	for _, c := range s.Manifest.Circuits {
		st, e := s.State(c.Spec.ID)
		if e != nil {
			return ref, e
		}
		if len(st.History) == 0 {
			return ref, fmt.Errorf("%s has no contributions", c.Spec.ID)
		}
		cp.Lifecycle.ClosedHeads[c.Spec.ID] = st.Head
	}
	cp.Lifecycle.Phase = "closed"
	cp.Lifecycle.ClosedAt = &now
	if err = s.commitCheckpoint(cp, ref); err != nil {
		return Artifact{}, err
	}
	_, ref, err = s.checkpoint()
	return ref, err
}
