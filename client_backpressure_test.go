package ceremony

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestContributionWaitsThroughUploadBackpressure(t *testing.T) {
	s := signedStore(t)
	api := s.Handler()
	var uploads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/contributions/") && uploads.Add(1) <= 5 {
			if r.Header.Get("Expect") != "100-continue" {
				t.Error("upload did not negotiate capacity before sending body")
			}
			w.Header().Set("Retry-After", "1")
			http.Error(w, "busy", 429)
			return
		}
		api.ServeHTTP(w, r)
	}))
	defer server.Close()
	i, err := s.InviteQueued(server.URL, "alice", Core, time.Hour)
	must(t, err)
	c := NewClient(t.TempDir())
	c.Progress = io.Discard
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	must(t, c.Run(ctx, i, Core))
	if uploads.Load() != 7 {
		t.Fatalf("expected five capacity responses and two accepted uploads, got %d", uploads.Load())
	}
	records, err := s.ContributionRecords()
	must(t, err)
	if len(records) != 1 || records[0].Total != 2 {
		t.Fatal("backpressure lost or duplicated a contribution")
	}
	_, err = VerifyLocalContributions(c.Work, i)
	must(t, err)
}

func TestUploadWaitHonorsCancellationAndPermanentFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := retryUpload(ctx, func() error { calls++; cancel(); return uploadBackpressure{wait: time.Minute} })
	if err != context.Canceled || calls != 1 {
		t.Fatal("capacity wait ignored cancellation")
	}
	calls = 0
	err = retryUpload(context.Background(), func() error { calls++; return permanentError{context.DeadlineExceeded} })
	if err == nil || calls != 1 {
		t.Fatal("retried a permanent rejection")
	}
}

func TestTenConcurrentContributorsUseSeparateCircuitReservations(t *testing.T) {
	s := signedStore(t)
	server := httptest.NewServer(s.HandlerWithOptions(ServerOptions{MaxUploads: 2, LeaseTTL: time.Minute}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	start := make(chan struct{})
	done := make(chan error, 10)
	for n := range 10 {
		i, err := s.InviteQueued(server.URL, fmt.Sprintf("contributor-%d", n), Core, time.Hour)
		must(t, err)
		client := NewClient(t.TempDir())
		client.Progress = io.Discard
		go func() { <-start; done <- client.Run(ctx, i, Core) }()
	}
	close(start)
	for range 10 {
		must(t, <-done)
	}
	records, err := s.ContributionRecords()
	must(t, err)
	if len(records) != 10 {
		t.Fatalf("got %d completed participants", len(records))
	}
	for _, circuit := range s.Manifest.Circuits {
		state, err := s.State(circuit.Spec.ID)
		must(t, err)
		if len(state.History) != 10 {
			t.Fatal("lost or duplicated contribution")
		}
	}
	must(t, s.Audit(Core, 64))
}
