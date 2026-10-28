package ceremony

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AuditPublic verifies a pinned core transcript without operator credentials or
// an invitation. Both hashes must be authenticated independently by the caller.
// The server's current checkpoint is never substituted for the requested one.
func AuditPublic(ctx context.Context, origin, work, manifestSHA, checkpointSHA string, maxDomain uint64, options AuditOptions) (RecoveryPoint, string, error) {
	var point RecoveryPoint
	if err := options.validate(); err != nil {
		return point, "", err
	}
	if err := ValidateURL(origin); err != nil {
		return point, "", err
	}
	if !validInvitationID(manifestSHA) || !validInvitationID(checkpointSHA) {
		return point, "", fmt.Errorf("authenticated manifest and checkpoint SHA256 hashes are required")
	}
	if strings.TrimSpace(work) == "" {
		return point, "", fmt.Errorf("choose a public audit workspace")
	}
	client := NewClient(work)
	var err error
	point.Manifest, err = pinnedPublicMetadata(ctx, client, origin, manifestSHA)
	if err != nil {
		return point, "", err
	}
	point.Checkpoint, err = pinnedPublicMetadata(ctx, client, origin, checkpointSHA)
	if err != nil {
		return point, "", err
	}
	// Reuse the same signed-core and production-parameter checks as operator
	// auditing. This path only fetches public objects; it never calls Status/admin.
	o := &Operator{Config: OperatorConfig{URL: origin, Manifest: point.Manifest}, client: client}
	if _, _, err = o.manifest(ctx); err != nil {
		return point, "", err
	}
	progress := options.Progress
	if progress == nil {
		progress = io.Discard
	}
	return withPublicSnapshot(ctx, client, origin, work, point, progress, func(s *Store) error {
		fmt.Fprintf(progress, "Verifying %d circuits, signatures and checkpoint history…\n", len(s.Manifest.Circuits))
		options.Progress = progress
		return s.AuditWithOptions(Core, maxDomain, options)
	})
}

// Resolve only the size of hash-pinned metadata; the origin cannot choose its
// identity. The bounded response is hashed before any transcript is downloaded.
func pinnedPublicMetadata(ctx context.Context, client *Client, origin, digest string) (Artifact, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(origin, "/")+"/v1/objects/"+digest, nil)
	if err != nil {
		return Artifact{}, fmt.Errorf("invalid public metadata request")
	}
	response, err := client.HTTP.Do(req)
	if err != nil {
		return Artifact{}, safeRequestError(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Artifact{}, fmt.Errorf("pinned public metadata unavailable: HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxJSONBytes+1))
	if err != nil || len(body) == 0 || len(body) > maxJSONBytes || hashBytes(body) != digest {
		return Artifact{}, fmt.Errorf("public metadata does not match its pinned hash or size limit")
	}
	return Artifact{SHA256: digest, Size: int64(len(body))}, nil
}
