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
	"strings"
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
	if i.Format != Format || i.Track != track || len(i.Jobs) == 0 || len(i.Token) != 64 || !safeID.MatchString(i.Contributor) {
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
	if e = decodeJSON(strings.NewReader(string(b)), &m); e != nil {
		return e
	}
	if m.Engine != Engine || m.Format != Format || m.Phase1.InsecureTestSetup != i.InsecureTestSetup {
		return fmt.Errorf("manifest engine or test-setup marker mismatch")
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
	if i.InsecureTestSetup {
		fmt.Fprintln(c.Progress, "REHEARSAL: coordinator-generated test SRS; outputs are not production keys")
	}
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
	return nil
}

func (c *Client) runJob(ctx context.Context, i Invitation, job Job, track string) error {
	var e error
	base := strings.TrimRight(i.URL, "/")
	id := job.Circuit.Spec.ID
	fmt.Fprintf(c.Progress, "[%s] %s: downloading parent %s\n", track, id, job.Input.SHA256[:12])
	dir := filepath.Join(c.Work, i.ManifestSHA256, i.Contributor, id, job.Input.SHA256)
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
		fmt.Fprintf(c.Progress, "[%s] %s: contributing fresh randomness\n", track, id)
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
	fmt.Fprintf(c.Progress, "[%s] %s: uploading and awaiting verification\n", track, id)
	var receipt Receipt
	if e = retry(ctx, func() error {
		f, e := os.Open(output)
		if e != nil {
			return e
		}
		defer f.Close()
		req, e := http.NewRequestWithContext(ctx, "POST", base+"/v1/contributions/"+id, f)
		if e != nil {
			return e
		}
		req.ContentLength = cached.Output.Size
		req.Header.Set("Authorization", "Bearer "+i.Token)
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, e := c.HTTP.Do(req)
		if e != nil {
			return e
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
			err := fmt.Errorf("upload rejected (%d): %s", resp.StatusCode, strings.TrimSpace(string(msg)))
			if resp.StatusCode >= 500 || resp.StatusCode == 429 {
				return err
			}
			return permanentError{err}
		}
		return decodeJSON(io.LimitReader(resp.Body, 1<<20), &receipt)
	}); e != nil {
		return e
	}
	if receipt.Circuit != id || receipt.Sequence != job.Sequence || receipt.Parent != job.Input || receipt.Output != cached.Output || receipt.Contributor != i.Contributor {
		return fmt.Errorf("acceptance receipt mismatch")
	}
	if e = WriteJSON(filepath.Join(dir, "receipt.json"), receipt); e != nil {
		return e
	}
	fmt.Fprintf(c.Progress, "[%s] %s: accepted #%d, SHA256 %s\n", track, id, receipt.Sequence, receipt.Output.SHA256)
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
			return e
		}
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}
		resp, e := c.HTTP.Do(req)
		if e != nil {
			return e
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
