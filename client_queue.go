package ceremony

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func (c *Client) queueRequest(ctx context.Context, i Invitation, path string) (Claim, error) {
	var claim Claim
	req, err := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(i.URL, "/")+path, nil)
	if err != nil {
		return claim, err
	}
	req.Header.Set("Authorization", "Bearer "+i.Token)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return claim, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return claim, fmt.Errorf("scheduling request rejected (HTTP %d); check invitation expiry and ceremony status", res.StatusCode)
	}
	err = decodeRemoteJSON(io.LimitReader(res.Body, 4<<20), &claim)
	return claim, err
}

func (c *Client) runQueue(ctx context.Context, i Invitation, track string) error {
	for {
		if !time.Now().Before(i.Expires) {
			return fmt.Errorf("invitation expired; request renewal using the same contributor name")
		}
		claim, err := c.queueRequest(ctx, i, "/v1/claim")
		if err != nil {
			return err
		}
		if claim.Done {
			if err = c.recoverReceipts(i, claim.Receipts); err != nil {
				return err
			}
			fmt.Fprintln(c.Progress, "All invited circuits have an accepted contribution.")
			return nil
		}
		if claim.Job == nil {
			if claim.WaitSeconds < 1 || claim.WaitSeconds > 60 {
				return fmt.Errorf("invalid queue wait")
			}
			fmt.Fprintln(c.Progress, "Waiting for an available circuit…")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(claim.WaitSeconds) * time.Second):
			}
			continue
		}
		job := *claim.Job
		found := false
		for _, scope := range i.Jobs {
			if sameCircuit(job.Circuit, scope.Circuit) {
				found = true
				break
			}
		}
		if !found || job.Input.Validate() != nil || job.Input.Size > c.MaxFileBytes || job.Sequence < 1 {
			return fmt.Errorf("invalid or oversized scheduled circuit")
		}
		if !time.Now().Before(claim.Expires) {
			return fmt.Errorf("reservation already expired")
		}
		jobCtx, cancel := context.WithCancel(ctx)
		done := make(chan error, 1)
		interval := time.Until(claim.Expires) / 3
		if interval > time.Minute {
			interval = time.Minute
		}
		if interval < time.Second {
			interval = time.Second
		}
		go func() {
			expires := claim.Expires
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-jobCtx.Done():
					done <- nil
					return
				case <-ticker.C:
					renewCtx, stop := context.WithTimeout(jobCtx, 30*time.Second)
					renewed, e := c.queueRequest(renewCtx, i, "/v1/heartbeat/"+job.Circuit.Spec.ID)
					stop()
					if jobCtx.Err() != nil {
						done <- nil
						return
					}
					if e != nil {
						// A coordinator commit may hold the mutation lock while
						// verifying or publishing. Keep the current lease through
						// transient heartbeat failures, but never past its expiry.
						if time.Now().Before(expires) {
							continue
						}
						done <- e
						cancel()
						return
					}
					if renewed.Done {
						done <- nil
						return
					}
					if !renewed.Expires.After(time.Now()) {
						done <- fmt.Errorf("expired reservation")
						cancel()
						return
					}
					expires = renewed.Expires
				}
			}
		}()
		err = c.runJob(jobCtx, i, job, track)
		cancel()
		heartbeatErr := <-done
		if err != nil {
			if heartbeatErr != nil {
				return fmt.Errorf("reservation renewal failed; completed local output is reusable: %w", heartbeatErr)
			}
			return err
		}
	}
}
