package ceremony

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ParseInvitationLink never includes the supplied link in an error. The fragment
// carries a bearer token and must not appear in logs, argv, or HTTP request URLs.
func ParseInvitationLink(raw string) (origin, token, pin string, err error) {
	invalid := func() (string, string, string, error) { return "", "", "", fmt.Errorf("invalid invitation link") }
	if len(raw) > 8192 {
		return invalid()
	}
	u, e := url.Parse(strings.TrimSpace(raw))
	if e != nil || u.User != nil || u.RawQuery != "" || u.Path != "" && u.Path != "/" {
		return invalid()
	}
	origin = u.Scheme + "://" + u.Host
	if ValidateURL(origin) != nil {
		return invalid()
	}
	fragment, e := url.ParseQuery(u.Fragment)
	if e != nil || len(fragment) != 2 || len(fragment["token"]) != 1 || len(fragment["manifest"]) != 1 {
		return invalid()
	}
	token, pin = fragment.Get("token"), fragment.Get("manifest")
	for _, value := range []string{token, pin} {
		if (Artifact{SHA256: value, Size: 1}).Validate() != nil {
			return invalid()
		}
	}
	return origin, token, pin, nil
}

// Join fetches the private invitation using a bearer header, authenticates its
// origin and manifest pin, then runs the same resumable native contribution flow.
// No private invitation is written to the public participant workspace.
func (c *Client) Join(ctx context.Context, link string) error {
	origin, token, pin, err := ParseInvitationLink(link)
	if err != nil {
		return err
	}
	lookup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(lookup, "GET", origin+"/v1/invitation", nil)
	if err != nil {
		return fmt.Errorf("cannot construct invitation request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("invitation lookup failed: %w", safeRequestError(err))
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("invitation lookup rejected (HTTP %d)", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, maxJSONBytes+1))
	if err != nil || len(body) > maxJSONBytes {
		return fmt.Errorf("invalid invitation response")
	}
	var invitation Invitation
	if decodeJSON(strings.NewReader(string(body)), &invitation) != nil {
		return fmt.Errorf("invalid invitation response")
	}
	clear(body)
	if invitation.Token != token || invitation.ManifestSHA256 != pin || strings.TrimRight(invitation.URL, "/") != origin || invitation.Mode != "queue" || invitation.Track != Core {
		return fmt.Errorf("invitation origin, scope, or manifest pin mismatch")
	}
	cancel()
	return c.Run(ctx, invitation, Core)
}

// net/url errors include the full URL by default, including presigned S3 query
// credentials. Expose the underlying transport failure without the URL.
func safeRequestError(err error) error {
	if e, ok := err.(*url.Error); ok {
		return e.Err
	}
	return err
}

func decodeRemoteJSON(r io.Reader, value any) error {
	if err := decodeJSON(r, value); err != nil {
		return fmt.Errorf("invalid coordinator response")
	}
	return nil
}
