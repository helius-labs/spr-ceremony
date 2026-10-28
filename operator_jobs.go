package ceremony

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type OperatorJobRequest struct {
	ID         string `json:"id"`
	Action     string `json:"action"`
	Checkpoint string `json:"checkpoint"`
	Hours      int    `json:"hours,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

type OperatorJob struct {
	Request  OperatorJobRequest `json:"request"`
	Status   string             `json:"status"`
	Started  time.Time          `json:"started"`
	Finished *time.Time         `json:"finished,omitempty"`
	Result   *Artifact          `json:"result,omitempty"`
	Error    string             `json:"error,omitempty"`
}

type operatorJobs struct {
	store     *Store
	chainInfo func(context.Context) ([]byte, error)
	beacon    func(context.Context, BeaconPlan) (BeaconProof, error)
}

func newOperatorJobs(store *Store) *operatorJobs {
	return &operatorJobs{store: store, chainInfo: func(ctx context.Context) ([]byte, error) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		r, err := http.NewRequestWithContext(ctx, "GET", "https://api2.drand.sh/"+QuicknetChainHash+"/info", nil)
		if err != nil {
			return nil, err
		}
		response, err := NewClient("").HTTP.Do(r)
		if err != nil {
			return nil, err
		}
		defer response.Body.Close()
		data, err := io.ReadAll(io.LimitReader(response.Body, 16385))
		if err != nil || response.StatusCode != 200 || len(data) > 16384 {
			return nil, fmt.Errorf("quicknet chain information unavailable")
		}
		return data, nil
	}, beacon: func(ctx context.Context, plan BeaconPlan) (BeaconProof, error) {
		return FetchBeacon(ctx, fmt.Sprintf("https://api2.drand.sh/%s/public/%d", plan.ChainHash, plan.Round), plan)
	}}
}

func (j *operatorJobs) directory() string {
	return filepath.Join(j.store.Dir, "private", "operator-jobs")
}
func (j *operatorJobs) path(id string) string { return filepath.Join(j.directory(), id+".json") }

func (j *operatorJobs) lock() (*os.File, error) {
	if err := os.MkdirAll(j.directory(), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(j.directory(), ".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("another coordinator operation is running")
	}
	return f, nil
}
func unlockOperatorJob(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }

func (j *operatorJobs) read(id string) (OperatorJob, error) {
	var job OperatorJob
	if !validInvitationID(id) {
		return job, fmt.Errorf("invalid operation ID")
	}
	if err := ReadJSON(j.path(id), &job); err != nil {
		return job, fmt.Errorf("operation unavailable")
	}
	if job.Request.ID != id {
		return job, fmt.Errorf("operation identity mismatch")
	}
	if job.Status == "running" {
		if lock, err := j.lock(); err == nil {
			defer unlockOperatorJob(lock)
			// The process owning a running operation has gone away. Finalization
			// is resumable from its published state and complete local export.
			if err = ReadJSON(j.path(id), &job); err != nil {
				return job, err
			}
			if job.Status == "running" {
				now := time.Now().UTC()
				job.Status, job.Finished, job.Error = "interrupted", &now, "Coordinator restarted. Repeat the operation to resume."
				if err = WriteJSON(j.path(id), job); err != nil {
					return job, err
				}
			}
		}
	}
	return job, nil
}

func (j *operatorJobs) start(input OperatorJobRequest) (OperatorJob, error) {
	var job OperatorJob
	if !validInvitationID(input.ID) || !validInvitationID(input.Checkpoint) {
		return job, fmt.Errorf("invalid operation identity or checkpoint")
	}
	switch input.Action {
	case "open", "recover-beacon":
		if input.Hours < 2 || input.Hours > 168 {
			return job, fmt.Errorf("choose a beacon 2–168 hours ahead")
		}
	case "close", "finalize":
		if input.Hours != 0 {
			return job, fmt.Errorf("unexpected beacon window")
		}
	default:
		return job, fmt.Errorf("unsupported coordinator operation")
	}
	if input.Action == "recover-beacon" {
		if len(input.Reason) < 1 || len(input.Reason) > 500 {
			return job, fmt.Errorf("recovery requires a short public reason")
		}
	} else if input.Reason != "" {
		return job, fmt.Errorf("unexpected recovery reason")
	}
	if _, err := os.Stat(j.path(input.ID)); err == nil {
		existing, err := j.read(input.ID)
		if err != nil {
			return job, err
		}
		if existing.Request != input {
			return job, fmt.Errorf("operation ID already has different parameters")
		}
		return existing, nil
	}
	lock, err := j.lock()
	if err != nil {
		return job, err
	}
	owned := true
	defer func() {
		if owned {
			unlockOperatorJob(lock)
		}
	}()
	// A concurrent identical request may have completed between the first file
	// lookup and acquiring the lock. Never execute that operation twice.
	if _, err = os.Stat(j.path(input.ID)); err == nil {
		existing, err := j.read(input.ID)
		if err != nil || existing.Request != input {
			return job, fmt.Errorf("operation ID already exists; inspect operator job")
		}
		return existing, nil
	} else if !os.IsNotExist(err) {
		return job, err
	}
	_, ref, err := j.store.Lifecycle()
	if err != nil || ref.SHA256 != input.Checkpoint {
		return job, fmt.Errorf("checkpoint changed; inspect operator status and retry")
	}
	job = OperatorJob{Request: input, Status: "running", Started: time.Now().UTC()}
	if err = WriteJSON(j.path(input.ID), job); err != nil {
		return job, err
	}
	if err = WriteJSON(filepath.Join(j.directory(), "latest.json"), map[string]string{"id": input.ID}); err != nil {
		return job, err
	}
	owned = false
	started := job
	go func() {
		defer unlockOperatorJob(lock)
		defer func() {
			if recover() != nil {
				job.Status, job.Error = "failed", "Coordinator operation failed. Inspect status before retrying."
			}
			now := time.Now().UTC()
			job.Finished = &now
			_ = WriteJSON(j.path(input.ID), job)
		}()
		result, err := j.execute(input)
		if err != nil {
			job.Status = "failed"
			// Underlying transport errors can contain signed URLs. Return fixed
			// guidance; never persist raw request errors in operation records.
			job.Error = map[string]string{
				"open":           "Opening failed. Check prepared status, signing key and future quicknet availability.",
				"close":          "Closure failed. Every circuit needs a contribution, reservations must finish, and the beacon must still be in the future.",
				"finalize":       "Finalization failed. Close first, wait for the exact planned beacon, then retry.",
				"recover-beacon": "Recovery failed. It requires an open campaign, a missed deadline and the current checkpoint.",
			}[input.Action]
			return
		}
		job.Status, job.Result = "completed", &result
	}()
	return started, nil
}

func futureQuicknetPlan(info []byte, hours int, now time.Time) (BeaconPlan, error) {
	var clock struct {
		Genesis int64 `json:"genesis_time"`
		Period  int64 `json:"period"`
	}
	if err := json.Unmarshal(info, &clock); err != nil || clock.Period < 1 || clock.Period > 3600 || clock.Genesis < 1 || clock.Genesis > now.Unix() {
		return BeaconPlan{}, fmt.Errorf("invalid quicknet clock")
	}
	round := uint64((now.Add(time.Duration(hours)*time.Hour).Unix()-clock.Genesis)/clock.Period + 2)
	return NewBeaconPlan(info, QuicknetChainHash, round)
}

func (j *operatorJobs) execute(input OperatorJobRequest) (Artifact, error) {
	s := j.store
	switch input.Action {
	case "open":
		info, err := j.chainInfo(context.Background())
		if err != nil {
			return Artifact{}, err
		}
		plan, err := futureQuicknetPlan(info, input.Hours, s.nowUTC())
		if err != nil {
			return Artifact{}, err
		}
		return s.OpenCeremony(plan)
	case "close":
		return s.CloseCeremony()
	case "recover-beacon":
		life, _, err := s.Lifecycle()
		if err != nil || life.Beacon == nil {
			return Artifact{}, fmt.Errorf("beacon plan unavailable")
		}
		plan, err := futureQuicknetPlan(life.Beacon.ChainInfo, input.Hours, s.nowUTC())
		if err != nil {
			return Artifact{}, err
		}
		return s.RecoverBeacon(plan.Round, input.Checkpoint, input.Reason)
	case "finalize":
		life, _, err := s.Lifecycle()
		if err != nil || life.Beacon == nil || s.nowUTC().Before(life.Beacon.RoundTime) {
			return Artifact{}, fmt.Errorf("planned beacon is not available yet")
		}
		proof, err := j.beacon(context.Background(), *life.Beacon)
		if err != nil {
			return Artifact{}, err
		}
		return s.Finalize(filepath.Join(s.Dir, "export", "core"), proof, 1<<22)
	}
	return Artifact{}, fmt.Errorf("unsupported coordinator operation")
}
