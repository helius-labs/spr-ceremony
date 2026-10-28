package ceremony

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"time"
)

// GitHubIdentity is a coordinator attestation of OAuth account control, not a
// participant signature or a claim about the participant's randomness.
// It is separate from the immutable contribution/checkpoint history.
type GitHubIdentity struct {
	Format         string    `json:"format"`
	Manifest       Artifact  `json:"manifest"`
	CoordinatorKey Artifact  `json:"coordinator_key"`
	InvitationID   string    `json:"invitation_id"`
	Contributor    string    `json:"contributor"`
	ReceiptsSHA256 string    `json:"receipts_sha256"`
	GitHubID       int64     `json:"github_id"`
	GitHubLogin    string    `json:"github_login"`
	LinkedAt       time.Time `json:"linked_at"`
	Signature      string    `json:"signature,omitempty"`
}

var githubLogin = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,37}[a-zA-Z0-9])?$`)

func receiptDigest(record ContributionRecord) string {
	// ContributionRecords sorts receipts by circuit. The digest excludes the
	// checkpoint, which can advance after this participant has completed.
	b, _ := json.Marshal(record.Receipts)
	return hashBytes(b)
}

func VerifyGitHubIdentity(identity GitHubIdentity, record ContributionRecord, key []byte) error {
	if identity.Format != "spr-github-identity-v1" || identity.Manifest != record.Manifest || identity.CoordinatorKey != record.CoordinatorKey || identity.InvitationID != record.InvitationID || identity.Contributor != record.Contributor || identity.ReceiptsSHA256 != receiptDigest(record) || identity.GitHubID <= 0 || !githubLogin.MatchString(identity.GitHubLogin) || identity.LinkedAt.IsZero() {
		return fmt.Errorf("GitHub identity does not match contribution receipts")
	}
	if int64(len(key)) != record.CoordinatorKey.Size || hashBytes(key) != record.CoordinatorKey.SHA256 {
		return fmt.Errorf("GitHub identity signing key mismatch")
	}
	return verifySignedJSON(key, "github-identity", record.Manifest.SHA256, identity)
}

func (s *Store) completedRecord(id string) (ContributionRecord, error) {
	records, err := s.ContributionRecords()
	if err != nil {
		return ContributionRecord{}, err
	}
	for _, record := range records {
		if record.InvitationID == id {
			return record, nil
		}
	}
	return ContributionRecord{}, fmt.Errorf("complete all invited core circuits before linking GitHub")
}

func (s *Store) githubIdentityPath(id string) string {
	return filepath.Join(s.Dir, "github-identities", id+".json")
}
func (s *Store) githubIdentityKey(id string) string {
	return s.remoteKey("github-identities/" + id + ".json")
}

// GitHubIdentityFor restores a missing sidecar from its immutable S3 pointer.
// No listing permission, invitation credentials or checkpoint mutation is needed.
func (s *Store) GitHubIdentityFor(ctx context.Context, record ContributionRecord) (*GitHubIdentity, error) {
	if !validInvitationID(record.InvitationID) || record.Manifest != s.ManifestArtifact {
		return nil, fmt.Errorf("invalid identity scope")
	}
	var ref Artifact
	err := ReadJSON(s.githubIdentityPath(record.InvitationID), &ref)
	if os.IsNotExist(err) && s.Remote != nil {
		ref, _, err = s.Remote.ReadPointer(ctx, s.githubIdentityKey(record.InvitationID))
	}
	if os.IsNotExist(err) || errors.Is(err, ErrRemoteMissing) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if ref.Validate() != nil || ref.Size > 32<<10 {
		return nil, fmt.Errorf("invalid GitHub identity artifact")
	}
	if err = Check(s.Object(ref), ref); err != nil && s.Remote != nil {
		err = s.Remote.GetObject(ctx, objectKey(ref), s.Object(ref), ref)
	}
	if err != nil {
		return nil, err
	}
	var identity GitHubIdentity
	if err = ReadJSON(s.Object(ref), &identity); err != nil {
		return nil, err
	}
	key, err := s.PublicSigningKey()
	if err != nil {
		return nil, err
	}
	if err = VerifyGitHubIdentity(identity, record, key); err != nil {
		return nil, err
	}
	// This is a public cache. It is intentionally not part of a historical
	// transcript snapshot and is not copied into checkpoint verification.
	if s.checkpointRef == nil {
		if err = WriteJSON(s.githubIdentityPath(record.InvitationID), ref); err != nil {
			return nil, err
		}
	}
	return &identity, nil
}

func (s *Store) saveGitHubIdentity(ctx context.Context, record ContributionRecord, id int64, login string) (*GitHubIdentity, error) {
	if s.checkpointRef != nil || id <= 0 || !githubLogin.MatchString(login) || s.Manifest.CoordinatorKey == nil {
		return nil, fmt.Errorf("invalid GitHub identity")
	}
	// Never hold the coordinator's contribution lock while talking to GitHub
	// or publishing this optional sidecar. Serialize identity writers separately.
	f, err := os.OpenFile(filepath.Join(s.Dir, "github-identities.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, fmt.Errorf("identity publication busy; retry")
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	previous, err := s.GitHubIdentityFor(ctx, record)
	if err != nil {
		return nil, err
	}
	if previous != nil {
		if previous.GitHubID != id {
			return nil, fmt.Errorf("this contribution is already linked to another GitHub account")
		}
		return previous, nil
	}
	identity := GitHubIdentity{Format: "spr-github-identity-v1", Manifest: record.Manifest, CoordinatorKey: record.CoordinatorKey, InvitationID: record.InvitationID, Contributor: record.Contributor, ReceiptsSHA256: receiptDigest(record), GitHubID: id, GitHubLogin: login, LinkedAt: s.nowUTC()}
	identity.Signature, err = s.signMessage("github-identity", identity)
	if err != nil {
		return nil, err
	}
	ref, err := s.putJSON(identity)
	if err != nil {
		return nil, err
	}
	if s.Remote != nil {
		if err = s.Remote.PutObject(ctx, objectKey(ref), s.Object(ref), ref); err != nil {
			return nil, ErrPublicationUnavailable
		}
		if err = s.Remote.ConditionalPut(ctx, s.githubIdentityKey(record.InvitationID), ref, ""); err != nil {
			// Resolve an ambiguous PUT or a competing writer from the remote
			// immutable pointer. A different account must never be overwritten.
			winner, readErr := s.GitHubIdentityFor(ctx, record)
			if readErr == nil && winner != nil && winner.GitHubID == id {
				return winner, nil
			}
			return nil, ErrPublicationUnavailable
		}
	}
	if err = WriteJSON(s.githubIdentityPath(record.InvitationID), ref); err != nil {
		return nil, err
	}
	return &identity, nil
}

func (s *Store) githubIdentityHandler(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !validInvitationID(id) {
		http.NotFound(w, r)
		return
	}
	record, err := s.completedRecord(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	identity, err := s.GitHubIdentityFor(ctx, record)
	if err != nil {
		http.Error(w, "GitHub identity unavailable", 503)
		return
	}
	if identity == nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	respond(w, identity)
}
