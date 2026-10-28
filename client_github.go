package ceremony

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// LinkGitHub creates a short-lived, single-use browser link. The invitation
// bearer stays in an Authorization header and never appears in the link.
func (c *Client) LinkGitHub(ctx context.Context, i Invitation) (string, error) {
	if ValidateURL(i.URL) != nil || !validInvitationID(i.Token) || !validInvitationID(i.ManifestSHA256) || i.Track != Core {
		return "", fmt.Errorf("valid core invitation required")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	origin := strings.TrimRight(i.URL, "/")
	if len(i.signingPublicKey) == 0 {
		data, err := c.githubPublicGet(ctx, origin+"/v1/manifest", maxJSONBytes)
		if err != nil || hashBytes(data) != i.ManifestSHA256 {
			return "", fmt.Errorf("GitHub linking manifest differs from invitation")
		}
		var manifest Manifest
		if json.Unmarshal(data, &manifest) != nil || manifest.CoordinatorKey == nil || manifest.CoordinatorKey.Validate() != nil || manifest.CoordinatorKey.Size > 16<<10 {
			return "", fmt.Errorf("GitHub linking requires a signed invitation")
		}
		public, err := c.githubPublicGet(ctx, origin+"/v1/objects/"+manifest.CoordinatorKey.SHA256, 16<<10)
		if err != nil || hashBytes(public) != manifest.CoordinatorKey.SHA256 || int64(len(public)) != manifest.CoordinatorKey.Size || verifySignedJSON(public, "invitation", i.ManifestSHA256, i) != nil {
			return "", fmt.Errorf("GitHub linking invitation signature is invalid")
		}
	}
	req, err := http.NewRequestWithContext(ctx, "POST", origin+"/v1/github/link", nil)
	if err != nil {
		return "", fmt.Errorf("cannot construct GitHub link request")
	}
	req.Header.Set("Authorization", "Bearer "+i.Token)
	response, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("GitHub linking unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode == 404 {
		return "", nil // Older/disabled coordinators do not affect contributions.
	}
	if response.StatusCode != 200 {
		return "", fmt.Errorf("GitHub linking rejected (HTTP %d); complete your contribution and use a valid invitation", response.StatusCode)
	}
	var result struct {
		URL     string    `json:"url"`
		Expires time.Time `json:"expires"`
	}
	if decodeRemoteJSON(io.LimitReader(response.Body, 4097), &result) != nil {
		return "", fmt.Errorf("invalid GitHub link response")
	}
	u, err := url.Parse(result.URL)
	if err != nil || u.Scheme+"://"+u.Host != origin || u.User != nil || u.Path != "/" || u.RawQuery != "" || !strings.HasPrefix(u.Fragment, "github=") || !validInvitationID(strings.TrimPrefix(u.Fragment, "github=")) || !result.Expires.After(time.Now()) || result.Expires.After(time.Now().Add(11*time.Minute)) {
		return "", fmt.Errorf("invalid GitHub link response")
	}
	return result.URL, nil
}

func (c *Client) githubPublicGet(ctx context.Context, address string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid public request")
	}
	response, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("public data unavailable")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || response.StatusCode != 200 || int64(len(body)) > limit {
		return nil, fmt.Errorf("invalid public response")
	}
	return body, nil
}

func (c *Client) offerGitHubLink(ctx context.Context, i Invitation) {
	if i.Track != Core || ctx.Err() != nil {
		return
	}
	link, err := c.LinkGitHub(ctx, i)
	if err != nil {
		fmt.Fprintln(c.Progress, "Contribution accepted. Optional GitHub linking is unavailable; retry with link-github later.")
	} else if link != "" {
		fmt.Fprintln(c.Progress, "Optional: link your GitHub account (private link, expires in 10 minutes):")
		fmt.Fprintln(c.Progress, link)
	}
}
