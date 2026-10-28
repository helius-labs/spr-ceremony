package ceremony

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FetchGitHubIdentities verifies optional account sidecars against already
// authenticated contribution records and their manifest-bound signing key.
func FetchGitHubIdentities(ctx context.Context, origin string, records []ContributionRecord) ([]GitHubIdentity, error) {
	if ValidateURL(origin) != nil {
		return nil, fmt.Errorf("invalid coordinator origin")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	client := NewClient("")
	base := strings.TrimRight(origin, "/")
	identities := []GitHubIdentity{}
	keys := map[Artifact][]byte{}
	for _, record := range records {
		if !validInvitationID(record.InvitationID) || record.CoordinatorKey.Validate() != nil || record.CoordinatorKey.Size > 16<<10 {
			return nil, fmt.Errorf("invalid contribution record")
		}
		req, err := http.NewRequestWithContext(ctx, "GET", base+"/v1/github/identities/"+record.InvitationID, nil)
		if err != nil {
			return nil, err
		}
		response, err := client.HTTP.Do(req)
		if err != nil {
			return nil, fmt.Errorf("GitHub identity download failed")
		}
		b, err := io.ReadAll(io.LimitReader(response.Body, 32<<10+1))
		response.Body.Close()
		if response.StatusCode == 404 {
			continue
		}
		var identity GitHubIdentity
		if err != nil || response.StatusCode != 200 || len(b) > 32<<10 || decodeJSON(bytes.NewReader(b), &identity) != nil {
			return nil, fmt.Errorf("invalid public GitHub identity")
		}
		key := keys[record.CoordinatorKey]
		if key == nil {
			key, err = client.githubPublicGet(ctx, base+"/v1/objects/"+record.CoordinatorKey.SHA256, 16<<10)
			if err != nil {
				return nil, err
			}
			keys[record.CoordinatorKey] = key
		}
		if err = VerifyGitHubIdentity(identity, record, key); err != nil {
			return nil, err
		}
		identities = append(identities, identity)
	}
	return identities, nil
}

func SaveGitHubIdentities(dir string, identities []GitHubIdentity) error {
	if dir == "" {
		return fmt.Errorf("GitHub identity output directory required")
	}
	for _, identity := range identities {
		if identity.Manifest.Validate() != nil || !validInvitationID(identity.InvitationID) || identity.Format != "spr-github-identity-v1" || identity.Signature == "" || identity.GitHubID <= 0 || !githubLogin.MatchString(identity.GitHubLogin) {
			return fmt.Errorf("invalid GitHub identity")
		}
		path := filepath.Join(dir, identity.Manifest.SHA256, identity.InvitationID+".json")
		var previous GitHubIdentity
		if err := ReadJSON(path, &previous); err == nil {
			a, _ := json.Marshal(previous)
			b, _ := json.Marshal(identity)
			if !bytes.Equal(a, b) {
				return fmt.Errorf("archived GitHub identity conflicts; do not overwrite")
			}
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(file)
		encoder.SetIndent("", "  ")
		err = encoder.Encode(identity)
		if err == nil {
			err = file.Sync()
		}
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			_ = os.Remove(path)
			return fmt.Errorf("could not archive GitHub identity")
		}
	}
	return nil
}
