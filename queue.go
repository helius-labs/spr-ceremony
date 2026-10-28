package ceremony

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Lease struct {
	Owner   string    `json:"owner"`
	Job     Job       `json:"job"`
	Expires time.Time `json:"expires"`
}
type Claim struct {
	Job         *Job      `json:"job,omitempty"`
	Expires     time.Time `json:"expires,omitempty"`
	Done        bool      `json:"done"`
	WaitSeconds int       `json:"wait_seconds,omitempty"`
	Receipts    []Receipt `json:"receipts,omitempty"`
}

func (s *Store) leasePath(id string) string { return filepath.Join(s.Dir, "leases", id+".json") }
func (s *Store) lease(token, id string) (Lease, error) {
	var l Lease
	if !safeID.MatchString(id) {
		return l, fmt.Errorf("invalid circuit ID")
	}
	if err := ReadJSON(s.leasePath(id), &l); err != nil {
		return l, err
	}
	if l.Owner != hashBytes([]byte(token)) || !s.nowUTC().Before(l.Expires) {
		return l, fmt.Errorf("reservation expired or belongs to another contributor")
	}
	return l, nil
}

func (s *Store) Claim(token string, ttl time.Duration) (Claim, error) {
	var result Claim
	if ttl <= 0 || ttl > 24*time.Hour {
		return result, fmt.Errorf("invalid reservation lifetime")
	}
	i, err := s.invitation(token)
	if err != nil {
		return result, err
	}
	if i.Mode != "queue" {
		return result, fmt.Errorf("invitation does not use scheduling")
	}
	unlock, err := s.lock()
	if err != nil {
		return result, err
	}
	defer unlock()
	if _, err = s.invitation(token); err != nil {
		return result, err
	}
	cp, _, err := s.checkpoint()
	if err != nil {
		return result, err
	}
	// Recover a receipt lost after acceptance, even after closure. This never
	// reserves work or changes ceremony state.
	completed := 0
	var receipts []Receipt
	for _, scope := range i.Jobs {
		st, e := s.State(scope.Circuit.Spec.ID)
		if e != nil {
			return result, e
		}
		found := false
		for _, r := range st.History {
			if r.InvitationID == i.ID {
				receipts = append(receipts, r)
				found = true
			}
		}
		if found {
			completed++
		}
	}
	if completed == len(i.Jobs) {
		return Claim{Done: true, Receipts: receipts}, nil
	}
	if cp.Lifecycle.Phase != "open" {
		return result, fmt.Errorf("ceremony is not open")
	}
	if cp.Lifecycle.Beacon != nil && !s.nowUTC().Before(cp.Lifecycle.Beacon.RoundTime) {
		return result, fmt.Errorf("announced beacon deadline has passed")
	}
	owner := hashBytes([]byte(token))
	var pending []Job
	for _, scope := range i.Jobs {
		st, e := s.State(scope.Circuit.Spec.ID)
		if e != nil {
			return result, e
		}
		complete := false
		for _, r := range st.History {
			if r.InvitationID == i.ID {
				complete = true
				break
			}
		}
		if complete {
			continue
		}
		job := Job{Circuit: scope.Circuit, Input: st.Head, Sequence: len(st.History) + 1}
		var l Lease
		e = ReadJSON(s.leasePath(scope.Circuit.Spec.ID), &l)
		if e == nil && s.nowUTC().Before(l.Expires) && l.Job.Input == st.Head {
			if l.Owner == owner {
				return Claim{Job: &l.Job, Expires: l.Expires}, nil
			}
			continue
		}
		if e != nil && !os.IsNotExist(e) {
			return result, e
		}
		pending = append(pending, job)
	}
	// Completion was checked above; remaining circuits are reserved by others.
	if len(pending) == 0 {
		return Claim{WaitSeconds: 5}, nil
	}
	job := pending[0]
	expires := s.nowUTC().Add(ttl)
	if expires.After(i.Expires) {
		expires = i.Expires
	}
	l := Lease{Owner: owner, Job: job, Expires: expires}
	if err = WriteJSON(s.leasePath(job.Circuit.Spec.ID), l); err != nil {
		return result, err
	}
	return Claim{Job: &job, Expires: expires}, nil
}

func (s *Store) Renew(token, id string, ttl time.Duration) (Claim, error) {
	var result Claim
	if ttl <= 0 || ttl > 24*time.Hour {
		return result, fmt.Errorf("invalid reservation lifetime")
	}
	i, err := s.invitation(token)
	if err != nil {
		return result, err
	}
	unlock, err := s.lock()
	if err != nil {
		return result, err
	}
	defer unlock()
	if _, err = s.invitation(token); err != nil {
		return result, err
	}
	cp, _, err := s.checkpoint()
	if err != nil {
		return result, err
	}
	allowed := false
	for _, job := range i.Jobs {
		if job.Circuit.Spec.ID == id {
			allowed = true
			break
		}
	}
	if i.Mode != "queue" || !allowed {
		return result, fmt.Errorf("circuit is not in queued invitation")
	}
	st, err := s.State(id)
	if err != nil {
		return result, err
	}
	// Acceptance may race the last heartbeat, including after closure.
	for _, receipt := range st.History {
		if receipt.InvitationID == i.ID {
			return Claim{Done: true}, nil
		}
	}
	if cp.Lifecycle.Phase != "open" {
		return result, fmt.Errorf("ceremony is not open")
	}
	if cp.Lifecycle.Beacon != nil && !s.nowUTC().Before(cp.Lifecycle.Beacon.RoundTime) {
		return result, fmt.Errorf("announced beacon deadline has passed")
	}
	l, err := s.lease(token, id)
	if err != nil {
		return result, err
	}
	if l.Job.Input != st.Head {
		return result, fmt.Errorf("circuit has advanced")
	}
	l.Expires = s.nowUTC().Add(ttl)
	if l.Expires.After(i.Expires) {
		l.Expires = i.Expires
	}
	if err = WriteJSON(s.leasePath(id), l); err != nil {
		return result, err
	}
	return Claim{Job: &l.Job, Expires: l.Expires}, nil
}
