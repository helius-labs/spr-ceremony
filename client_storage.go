package ceremony

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func (c *Client) downloadTicket(ctx context.Context, base string, a Artifact) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/v1/download/"+a.SHA256, nil)
	if err != nil {
		return "", err
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("download ticket unavailable: HTTP %d", res.StatusCode)
	}
	var ticket DownloadTicket
	if err = decodeRemoteJSON(io.LimitReader(res.Body, 64<<10), &ticket); err != nil {
		return "", err
	}
	if ticket.Artifact != a {
		return "", fmt.Errorf("download ticket artifact mismatch")
	}
	if ticket.URL == "/v1/objects/"+a.SHA256 {
		return base + ticket.URL, nil
	}
	u, err := url.Parse(ticket.URL)
	if err != nil || u.User != nil || u.Fragment != "" || u.Host == "" {
		return "", fmt.Errorf("invalid artifact URL")
	}
	if err = ValidateURL(u.Scheme + "://" + u.Host); err != nil {
		return "", err
	}
	if u.Scheme != "https" && !strings.HasPrefix(base, "http://") {
		return "", fmt.Errorf("HTTPS coordinator cannot redirect artifacts to HTTP")
	}
	return ticket.URL, nil
}
