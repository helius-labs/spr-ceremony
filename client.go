package ceremony

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Client struct {
	HTTP         *http.Client
	Work         string
	MaxFileBytes int64
	Progress     io.Writer
}

func NewClient(work string) *Client {
	return &Client{HTTP: &http.Client{Timeout: 24 * time.Hour, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}, Work: work, MaxFileBytes: 1 << 30, Progress: os.Stderr}
}

func ValidateURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return fmt.Errorf("coordinator URL must be an HTTPS origin")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if u.Scheme != "https" && !(u.Scheme == "http" && (host == "localhost" || ip != nil && ip.IsLoopback())) {
		return fmt.Errorf("HTTP is allowed only on loopback; use HTTPS")
	}
	return nil
}

func (c *Client) Run(ctx context.Context, i Invitation, track string) error {
	if i.Format != Format || i.Track != track || len(i.Jobs) == 0 || len(i.Token) != 64 || !safeID.MatchString(i.Contributor) || !validInvitationID(i.ID) {
		return fmt.Errorf("invalid invitation or track; batch-4999 requires explicit --track batch-4999")
	}
	if track != Core && track != Large {
		return fmt.Errorf("invalid track")
	}
	if time.Now().After(i.Expires) {
		return fmt.Errorf("invitation expired; request another invitation")
	}
	if e := ValidateURL(i.URL); e != nil {
		return e
	}
	base := strings.TrimRight(i.URL, "/")
	// Authenticate the entire manifest against the digest delivered in the invitation.
	req, e := http.NewRequestWithContext(ctx, "GET", base+"/v1/manifest", nil)
	if e != nil {
		return e
	}
	resp, e := c.HTTP.Do(req)
	if e != nil {
		return e
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	resp.Body.Close()
	if e != nil {
		return e
	}
	if resp.StatusCode != 200 || hashBytes(b) != i.ManifestSHA256 {
		return fmt.Errorf("coordinator manifest does not match invitation")
	}
	var m Manifest
	if e = decodeRemoteJSON(strings.NewReader(string(b)), &m); e != nil {
		return e
	}
	if m.Engine != Engine || m.Format != Format || m.Phase1.InsecureTestSetup != i.InsecureTestSetup {
		return fmt.Errorf("manifest engine or test-setup marker mismatch")
	}
	if e = c.authenticateSigningKey(ctx, &i, m, b); e != nil {
		return e
	}
	seen := map[string]bool{}
	for _, job := range i.Jobs {
		id := job.Circuit.Spec.ID

		if !safeID.MatchString(id) || seen[id] || job.Circuit.Spec.Track != track {
			return fmt.Errorf("invalid circuit in invitation")
		}
		seen[id] = true
		found := false
		for _, entry := range m.Circuits {
			if sameCircuit(entry, job.Circuit) {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("invitation circuit does not match manifest")
		}
		if e = job.Input.Validate(); e != nil {
			return e
		}
		if job.Input.Size > c.MaxFileBytes {
			return fmt.Errorf("%s input is %d bytes, above --max-file-bytes %d", id, job.Input.Size, c.MaxFileBytes)
		}
	}
	if m.Phase1.UsesTestParameters() {
		fmt.Fprintln(c.Progress, "Locally generated parameters. Do not use these keys in production.")
	}
	// Reusing one public workspace is safe across reruns, not concurrent writers.
	workspace := filepath.Join(c.Work, i.ManifestSHA256, i.ID)
	if e = os.MkdirAll(workspace, 0700); e != nil {
		return e
	}
	lock, e := os.OpenFile(filepath.Join(workspace, ".participant.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return fmt.Errorf("another client is using this participant workspace")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if i.Mode == "queue" {
		return c.runQueue(ctx, i, track)
	}
	if i.Mode != "" {
		return fmt.Errorf("unsupported invitation mode")
	}
	for _, job := range i.Jobs {
		if e = c.runJob(ctx, i, job, track); e != nil {
			return e
		}
	}
	c.offerGitHubLink(ctx, i)
	return nil
}

func (c *Client) runJob(ctx context.Context, i Invitation, job Job, track string) error {
	var e error
	base := strings.TrimRight(i.URL, "/")
	id := job.Circuit.Spec.ID
	index := 0
	for n, scope := range i.Jobs {
		if scope.Circuit.Spec.ID == id {
			index = n + 1
			break
		}
	}
	if index == 0 {
		return fmt.Errorf("circuit is outside the invitation")
	}
	progress := fmt.Sprintf("%d/%d", index, len(i.Jobs))
	fmt.Fprintf(c.Progress, "[%s] %s: downloading parent %s\n", progress, id, job.Input.SHA256[:12])
	dir := filepath.Join(c.Work, i.ManifestSHA256, i.ID, id, job.Input.SHA256)
	if e = os.MkdirAll(dir, 0700); e != nil {
		return e
	}
	if e = CheckDiskSpace(dir, uint64(job.Input.Size)*3+(64<<20)); e != nil {
		return fmt.Errorf("%s disk preflight: %w", id, e)
	}
	input := filepath.Join(dir, "input.ph2")
	output := filepath.Join(dir, "output.ph2")
	if e = c.downloadWithURL(ctx, func(ctx context.Context) (string, error) { return c.downloadTicket(ctx, base, job.Input) }, input, job.Input); e != nil {
		return e
	}
	var cached struct {
		Input  Artifact `json:"input"`
		Output Artifact `json:"output"`
	}
	reuse := ReadJSON(output+".json", &cached) == nil && cached.Input == job.Input && Check(output, cached.Output) == nil
	if !reuse {
		fmt.Fprintf(c.Progress, "[%s] %s: contributing fresh randomness\n", progress, id)
		if e = Contribute(input, output, job.Circuit.Shape); e != nil {
			return e
		}
		cached.Input = job.Input
		cached.Output, e = Digest(output)
		if e != nil {
			return e
		}
		if e = WriteJSON(output+".json", cached); e != nil {
			return e
		}
	}
	fmt.Fprintf(c.Progress, "[%s] %s: uploading and awaiting verification\n", progress, id)
	var receipt Receipt
	uploadDeadline := time.Now().Add(30 * time.Minute)
	if i.Expires.Before(uploadDeadline) {
		uploadDeadline = i.Expires
	}
	uploadCtx, stopUpload := context.WithDeadline(ctx, uploadDeadline)
	defer stopUpload()
	waitingPrinted := false
	if e = retryUpload(uploadCtx, func() error {
		f, e := os.Open(output)
		if e != nil {
			return e
		}
		defer f.Close()
		req, e := http.NewRequestWithContext(uploadCtx, "POST", base+"/v1/contributions/"+id, f)
		if e != nil {
			return e
		}
		req.ContentLength = cached.Output.Size
		req.Header.Set("Authorization", "Bearer "+i.Token)
		req.Header.Set("Content-Type", "application/octet-stream")
		// Let a busy server reject the upload before transmitting its body.
		req.Header.Set("Expect", "100-continue")
		resp, e := c.HTTP.Do(req)
		if e != nil {
			return e
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			err := fmt.Errorf("upload rejected (HTTP %d); rerun to recover or request invitation renewal", resp.StatusCode)
			if resp.StatusCode == 429 {
				if !waitingPrinted {
					fmt.Fprintf(c.Progress, "[%s] Coordinator busy; waiting for an upload slot…\n", progress)
					waitingPrinted = true
				}
				seconds, parseErr := strconv.Atoi(resp.Header.Get("Retry-After"))
				if parseErr != nil || seconds < 1 || seconds > 60 {
					seconds = 5
				}
				return uploadBackpressure{wait: time.Duration(seconds) * time.Second}
			}
			if resp.StatusCode >= 500 {
				return err
			}
			return permanentError{err}
		}
		return decodeRemoteJSON(io.LimitReader(resp.Body, 1<<20), &receipt)
	}); e != nil {
		return e
	}
	if receipt.Circuit != id || receipt.Sequence != job.Sequence || receipt.Parent != job.Input || receipt.Output != cached.Output || receipt.Contributor != i.Contributor || receipt.InvitationID != i.ID {
		return fmt.Errorf("acceptance receipt mismatch")
	}
	if e = retrySignatureTimeWindow(ctx, func() error { return c.verifyReceipt(i, receipt) }, func() {
		fmt.Fprintf(c.Progress, "[%s] %s: waiting briefly to verify receipt timestamp…\n", progress, id)
	}); e != nil {
		return e
	}
	if e = WriteJSON(filepath.Join(dir, "receipt.json"), receipt); e != nil {
		return e
	}
	fmt.Fprintf(c.Progress, "[%s] %s: accepted #%d, SHA256 %s\n", progress, id, receipt.Sequence, receipt.Output.SHA256)
	return nil
}

// HTTP Range resumes transfer bytes only. Input bytes are authenticated before
// use. A server that ignores Range safely restarts the download.
func (c *Client) download(ctx context.Context, raw, path string, a Artifact) error {
	return c.downloadWithURL(ctx, func(context.Context) (string, error) { return raw, nil }, path, a)
}

func (c *Client) downloadWithURL(ctx context.Context, resolve func(context.Context) (string, error), path string, a Artifact) error {
	if Check(path, a) == nil {
		return nil
	}
	part := path + ".part"
	return retry(ctx, func() error {
		raw, e := resolve(ctx)
		if e != nil {
			return e
		}
		var offset int64
		if st, e := os.Stat(part); e == nil {
			offset = st.Size()
		}
		if offset > a.Size {
			os.Remove(part)
			offset = 0
		}
		if offset == a.Size {
			if e := Check(part, a); e != nil {
				os.Remove(part)
				return e
			}
			return os.Rename(part, path)
		}
		req, e := http.NewRequestWithContext(ctx, "GET", raw, nil)
		if e != nil {
			return fmt.Errorf("invalid artifact request")
		}
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}
		resp, e := c.HTTP.Do(req)
		if e != nil {
			return safeRequestError(e)
		}
		defer resp.Body.Close()
		if resp.StatusCode == 200 {
			offset = 0
		} else if resp.StatusCode == 206 {
			want := fmt.Sprintf("bytes %d-%d/%d", offset, a.Size-1, a.Size)
			if resp.Header.Get("Content-Range") != want {
				return permanentError{fmt.Errorf("invalid Content-Range")}
			}
		} else {
			if resp.StatusCode == 403 || resp.StatusCode == 429 || resp.StatusCode >= 500 {
				return fmt.Errorf("download failed: HTTP %d", resp.StatusCode)
			}
			return permanentError{fmt.Errorf("download failed: HTTP %d", resp.StatusCode)}
		}
		f, e := os.OpenFile(part, os.O_CREATE|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		if e = f.Truncate(offset); e != nil {
			f.Close()
			return e
		}
		if _, e = f.Seek(offset, io.SeekStart); e != nil {
			f.Close()
			return e
		}
		n, e := io.Copy(f, io.LimitReader(resp.Body, a.Size-offset+1))
		ce := f.Close()
		if e != nil {
			return e
		}
		if ce != nil {
			return ce
		}
		if n != a.Size-offset {
			return fmt.Errorf("incomplete or oversized download")
		}
		if e = Check(part, a); e != nil {
			os.Remove(part)
			return e
		}
		return os.Rename(part, path)
	})
}

type permanentError struct{ error }

type uploadBackpressure struct{ wait time.Duration }

func (uploadBackpressure) Error() string { return "coordinator upload capacity reached" }

// Capacity is a queue condition, not one of the four transient network retry
// attempts. The caller bounds the wait by cancellation and invitation expiry.
func retryUpload(ctx context.Context, fn func() error) error {
	for failures := 0; ; {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := fn()
		if err == nil {
			return nil
		}
		var delay time.Duration
		if busy, ok := err.(uploadBackpressure); ok {
			delay = busy.wait
		} else {
			failures++
			if _, permanent := err.(permanentError); permanent || failures >= 4 {
				return err
			}
			delay = time.Duration(1<<(failures-1)) * time.Second
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func retry(ctx context.Context, fn func() error) error {
	var e error
	for attempt := 0; attempt < 4; attempt++ {
		e = fn()
		if e == nil {
			return nil
		}
		if _, ok := e.(permanentError); ok {
			return e
		}
		if attempt < 3 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(1<<attempt) * time.Second):
			}
		}
	}
	return e
}
