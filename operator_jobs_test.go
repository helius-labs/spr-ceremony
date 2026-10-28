package ceremony

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func waitOperatorJob(t *testing.T, jobs *operatorJobs, id string) OperatorJob {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		job, err := jobs.read(id)
		must(t, err)
		if job.Status != "running" {
			return job
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("coordinator job did not finish")
	return OperatorJob{}
}

func TestOperatorJobsOpenQuicknetAndPreserveRequestIdentity(t *testing.T) {
	s, _, _ := operatorFixture(t)
	cp, _, err := s.checkpoint()
	must(t, err)
	cp.Lifecycle = Lifecycle{Phase: "prepared"}
	ref, err := s.putJSON(cp)
	must(t, err)
	must(t, WriteJSON(filepath.Join(s.Dir, "checkpoint.json"), ref))
	var fixture struct {
		ChainInfo json.RawMessage `json:"chain_info"`
	}
	data, err := os.ReadFile("docs/evidence/live-beacon.json")
	must(t, err)
	must(t, json.Unmarshal(data, &fixture))
	jobs := newOperatorJobs(s)
	started, release := make(chan struct{}), make(chan struct{})
	jobs.chainInfo = func(context.Context) ([]byte, error) { close(started); <-release; return fixture.ChainInfo, nil }
	request := OperatorJobRequest{ID: strings.Repeat("1", 64), Action: "open", Checkpoint: ref.SHA256, Hours: 24}
	job, err := jobs.start(request)
	must(t, err)
	if job.Status != "running" {
		t.Fatal("job did not start asynchronously")
	}
	<-started
	duplicate, err := jobs.start(request)
	must(t, err)
	if duplicate.Request != request {
		t.Fatal("retry changed operation identity")
	}
	other := request
	other.ID = strings.Repeat("2", 64)
	if _, err = jobs.start(other); err == nil {
		t.Fatal("concurrent coordinator operation accepted")
	}
	changed := request
	changed.Hours = 48
	if _, err = jobs.start(changed); err == nil {
		t.Fatal("operation ID reused with different parameters")
	}
	close(release)
	job = waitOperatorJob(t, jobs, request.ID)
	if job.Status != "completed" {
		t.Fatal("quicknet opening failed")
	}
	life, checkpoint, err := s.Lifecycle()
	must(t, err)
	if life.Phase != "open" || life.Beacon == nil || life.Beacon.ChainHash != QuicknetChainHash || job.Result == nil || *job.Result != checkpoint {
		t.Fatal("wrong opening checkpoint or beacon")
	}
	st, err := os.Stat(jobs.path(request.ID))
	must(t, err)
	if st.Mode().Perm()&0077 != 0 {
		t.Fatal("operation state is not private")
	}
	if err = s.Audit(Core, 64); err != nil {
		t.Fatal("opening did not preserve a valid transcript")
	}
}

func TestOperatorJobsCloseAndFinalizeWithoutASession(t *testing.T) {
	s, operator, _ := operatorFixture(t)
	cp, _, err := s.checkpoint()
	must(t, err)
	cp.Lifecycle = Lifecycle{Phase: "prepared"}
	ref, err := s.putJSON(cp)
	must(t, err)
	must(t, WriteJSON(filepath.Join(s.Dir, "checkpoint.json"), ref))
	now := time.Now().UTC().Truncate(time.Second)
	plan, proof := testBeacon(t, now.Add(2*time.Hour))
	s.now = func() time.Time { return now }
	_, err = s.OpenCeremony(plan)
	must(t, err)
	invitation, err := operator.Invite(context.Background(), "first", 24)
	must(t, err)
	client := NewClient(t.TempDir())
	client.Progress = io.Discard
	must(t, client.Run(context.Background(), invitation, Core))
	jobs := newOperatorJobs(s)
	jobs.auditOptions = AuditOptions{Workers: 2}
	jobs.beacon = func(context.Context, BeaconPlan) (BeaconProof, error) { return proof, nil }
	_, ref, err = s.Lifecycle()
	must(t, err)
	job, err := jobs.start(OperatorJobRequest{ID: strings.Repeat("3", 64), Action: "close", Checkpoint: ref.SHA256})
	must(t, err)
	if waitOperatorJob(t, jobs, job.Request.ID).Status != "completed" {
		t.Fatal("closure failed")
	}
	s.now = func() time.Time { return plan.RoundTime.Add(time.Second) }
	_, ref, err = s.Lifecycle()
	must(t, err)
	job, err = jobs.start(OperatorJobRequest{ID: strings.Repeat("4", 64), Action: "finalize", Checkpoint: ref.SHA256})
	must(t, err)
	if waitOperatorJob(t, jobs, job.Request.ID).Status != "completed" {
		t.Fatal("finalization failed")
	}
	must(t, s.VerifyExport(filepath.Join(s.Dir, "export", "core"), 64))
	// An interrupted process is detected from the released lock; public state
	// remains authoritative and a subsequent finalization is idempotent.
	_, ref, err = s.Lifecycle()
	must(t, err)
	interrupted := OperatorJob{Request: OperatorJobRequest{ID: strings.Repeat("5", 64), Action: "finalize", Checkpoint: ref.SHA256}, Status: "running"}
	must(t, WriteJSON(jobs.path(interrupted.Request.ID), interrupted))
	got, err := jobs.read(interrupted.Request.ID)
	must(t, err)
	if got.Status != "interrupted" {
		t.Fatal("abandoned operation remained running")
	}
	job, err = jobs.start(OperatorJobRequest{ID: strings.Repeat("6", 64), Action: "finalize", Checkpoint: ref.SHA256})
	must(t, err)
	if waitOperatorJob(t, jobs, job.Request.ID).Status != "completed" {
		t.Fatal("finalization retry failed")
	}
}
