package ceremony

import (
	"bytes"
	"context"
	"crypto"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp/packet"
	openpgp "github.com/ProtonMail/go-crypto/openpgp/v2"
	"github.com/helius-labs/spr-ceremony/internal/signing"
)

func TestSignatureRetryBoundAndCancellation(t *testing.T) {
	for _, cancelAfter := range []time.Duration{0, 2 * time.Second} {
		synctest.Test(t, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelAfter != 0 {
				go func() { time.Sleep(cancelAfter); cancel() }()
			}
			started := time.Now()
			calls, announcements := 0, 0
			err := retrySignatureTimeWindow(ctx, func() error { calls++; return signing.ErrSignatureTimeWindow }, func() { announcements++ })
			if announcements != 1 {
				t.Fatalf("wait announcements: %d", announcements)
			}
			if cancelAfter == 0 {
				if !errors.Is(err, signing.ErrSignatureTimeWindow) || time.Since(started) != 10*time.Second || calls != 11 {
					t.Fatalf("unbounded/incorrect retry: %v, %s, %d calls", err, time.Since(started), calls)
				}
			} else if !errors.Is(err, context.Canceled) || time.Since(started) != cancelAfter {
				t.Fatalf("cancellation delayed: %v, %s", err, time.Since(started))
			}
		})
	}
}

type receiptTimeTransport struct {
	handler    http.Handler
	futureSign func(*Receipt)
	uploads    int
}

// In-process requests have finite local bodies and no network read deadline.
type receiptTimeRecorder struct{ *httptest.ResponseRecorder }

func (receiptTimeRecorder) SetReadDeadline(time.Time) error { return nil }

func (r *receiptTimeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	r.handler.ServeHTTP(receiptTimeRecorder{w}, req)
	response := w.Result()
	if req.Method == "POST" && strings.HasPrefix(req.URL.Path, "/v1/contributions/") && response.StatusCode == http.StatusOK {
		r.uploads++
		var receipt Receipt
		if err := json.NewDecoder(response.Body).Decode(&receipt); err != nil {
			return nil, err
		}
		response.Body.Close()
		r.futureSign(&receipt)
		b, err := json.Marshal(receipt)
		if err != nil {
			return nil, err
		}
		response.Body = io.NopCloser(bytes.NewReader(b))
		response.ContentLength = int64(len(b))
	}
	return response, nil
}

func TestClientWaitsForSignedReceiptWithoutReupload(t *testing.T) {
	for _, tamper := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			s := signedStore(t)
			private, err := os.ReadFile(filepath.Join(s.Dir, "private", "coordinator.private.asc"))
			must(t, err)
			keys, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(private))
			must(t, err)
			transport := &receiptTimeTransport{handler: s.Handler()}
			transport.futureSign = func(r *Receipt) {
				b, err := json.Marshal(r)
				must(t, err)
				payload, err := signing.Payload("receipt", s.ManifestArtifact.SHA256, b)
				must(t, err)
				created := time.Now().Add(2 * time.Second)
				var signature bytes.Buffer
				must(t, openpgp.ArmoredDetachSign(&signature, keys, bytes.NewReader(payload), &openpgp.SignParams{Config: &packet.Config{DefaultHash: crypto.SHA256, Time: func() time.Time { return created }}}))
				r.Signature = signature.String()
				if tamper {
					r.AcceptedAt = r.AcceptedAt.Add(time.Second)
				}
			}
			i, err := s.Invite("https://coordinator.example", "clock-test", Core, time.Hour)
			must(t, err)
			c := NewClient(t.TempDir())
			c.HTTP.Transport = transport
			var progress bytes.Buffer
			c.Progress = &progress
			started := time.Now()
			err = c.Run(context.Background(), i, Core)
			if tamper {
				if err == nil || errors.Is(err, signing.ErrSignatureTimeWindow) || time.Since(started) != 0 || transport.uploads != 1 {
					t.Fatalf("bad signature was retried or accepted: %v, %s, uploads=%d", err, time.Since(started), transport.uploads)
				}
				receipts, _ := filepath.Glob(filepath.Join(c.Work, i.ManifestSHA256, i.ID, "*", "*", "receipt.json"))
				if len(receipts) != 0 {
					t.Fatal("unverified receipt persisted")
				}
				if strings.Contains(progress.String(), "waiting briefly") {
					t.Fatal("cryptographic error treated as clock skew")
				}
				return
			}
			must(t, err)
			if transport.uploads != len(i.Jobs) {
				t.Fatalf("reuploaded work: %d uploads for %d jobs", transport.uploads, len(i.Jobs))
			}
			if time.Since(started) != time.Duration(len(i.Jobs))*2*time.Second {
				t.Fatalf("unexpected wait %s", time.Since(started))
			}
			if strings.Count(progress.String(), "waiting briefly") != len(i.Jobs) {
				t.Fatal("missing receipt wait progress")
			}
			for _, job := range i.Jobs {
				var r Receipt
				must(t, ReadJSON(filepath.Join(c.Work, i.ManifestSHA256, i.ID, job.Circuit.Spec.ID, job.Input.SHA256, "receipt.json"), &r))
				public, err := s.PublicSigningKey()
				must(t, err)
				must(t, verifySignedJSON(public, "receipt", i.ManifestSHA256, r))
				state, err := s.State(job.Circuit.Spec.ID)
				must(t, err)
				if len(state.History) != 1 {
					t.Fatal("retry duplicated a contribution")
				}
			}
		})
	}
}
