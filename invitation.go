package ceremony

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func validInvitationID(id string) bool {
	return strings.ToLower(id) == id && (Artifact{SHA256: id, Size: 1}).Validate() == nil
}
func randomIdentity() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (s *Store) Invite(url, name, track string, ttl time.Duration) (Invitation, error) {
	return s.invite(url, name, track, ttl, "", "")
}
func (s *Store) InviteQueued(url, name, track string, ttl time.Duration) (Invitation, error) {
	return s.invite(url, name, track, ttl, "queue", "")
}

// ReissueInvitation rotates the private bearer token while explicitly retaining
// a public invitation identity. A new invitation with the same pseudonym never
// inherits this identity or its accepted contributions.
func (s *Store) ReissueInvitation(url, id string, ttl time.Duration) (Invitation, error) {
	if !validInvitationID(id) {
		return Invitation{}, fmt.Errorf("invalid invitation ID")
	}
	return s.invite(url, "", "", ttl, "queue", id)
}

func (s *Store) storedInvitations() ([]Invitation, error) {
	entries, err := os.ReadDir(filepath.Join(s.Dir, "invitations"))
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	var invitations []Invitation
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		var i Invitation
		if err := ReadJSON(filepath.Join(s.Dir, "invitations", entry.Name()), &i); err != nil {
			return nil, err
		}
		if entry.Name() != hashBytes([]byte(i.Token))+".json" || !validInvitationID(i.ID) {
			return nil, fmt.Errorf("invalid stored invitation")
		}
		invitations = append(invitations, i)
	}
	return invitations, nil
}

func (s *Store) invite(url, name, track string, ttl time.Duration, mode, resumeID string) (Invitation, error) {
	if err := ValidateURL(url); err != nil {
		return Invitation{}, err
	}
	if ttl <= 0 || ttl > 7*24*time.Hour {
		return Invitation{}, fmt.Errorf("invitation lifetime must be at most seven days")
	}
	unlock, err := s.lock()
	if err != nil {
		return Invitation{}, err
	}
	defer unlock()
	cp, _, err := s.checkpoint()
	if err != nil {
		return Invitation{}, err
	}
	if cp.Lifecycle.Phase != "open" {
		return Invitation{}, fmt.Errorf("ceremony is not open")
	}
	if cp.Lifecycle.Beacon != nil && !s.nowUTC().Before(cp.Lifecycle.Beacon.RoundTime) {
		return Invitation{}, fmt.Errorf("announced beacon deadline has passed")
	}
	stored, err := s.storedInvitations()
	if err != nil {
		return Invitation{}, err
	}
	id := resumeID
	if resumeID != "" {
		// Retained public identity metadata permits retry after a failed rotation.
		// Following S3 recovery, accepted receipts supply the same identity.
		var identity struct {
			Contributor string `json:"contributor"`
			Track       string `json:"track"`
		}
		err = ReadJSON(filepath.Join(s.Dir, "identities", id+".json"), &identity)
		if err != nil && !os.IsNotExist(err) {
			return Invitation{}, err
		}
		name, track = identity.Contributor, identity.Track
		for _, old := range stored {
			if old.ID == id {
				name, track = old.Contributor, old.Track
			}
		}
		for _, c := range s.Manifest.Circuits {
			st, e := s.State(c.Spec.ID)
			if e != nil {
				return Invitation{}, e
			}
			for _, r := range st.History {
				if r.InvitationID == id {
					if name != "" && (name != r.Contributor || track != c.Spec.Track) {
						return Invitation{}, fmt.Errorf("inconsistent invitation identity")
					}
					name, track = r.Contributor, c.Spec.Track
				}
			}
		}
		if name == "" {
			return Invitation{}, fmt.Errorf("unknown invitation ID; create a new invitation")
		}
	} else {
		id, err = randomIdentity()
		if err != nil {
			return Invitation{}, err
		}
	}
	if !safeID.MatchString(name) || (track != Core && track != Large) {
		return Invitation{}, fmt.Errorf("invalid contributor or track")
	}
	for _, old := range stored {
		if old.ID != id && old.Contributor == name && s.nowUTC().Before(old.Expires) {
			return Invitation{}, fmt.Errorf("pseudonym already has an active invitation; reissue its ID or revoke it first")
		}
	}
	token, err := randomIdentity()
	if err != nil {
		return Invitation{}, err
	}
	i := Invitation{ID: id, Format: Format, URL: url, Token: token, ManifestSHA256: s.ManifestArtifact.SHA256, Contributor: name, Track: track, Expires: s.nowUTC().Add(ttl), InsecureTestSetup: s.Manifest.Phase1.InsecureTestSetup, Mode: mode}
	for _, c := range s.Manifest.Circuits {
		if c.Spec.Track == track {
			st, e := s.State(c.Spec.ID)
			if e != nil {
				return Invitation{}, e
			}
			i.Jobs = append(i.Jobs, Job{Circuit: c, Input: st.Head, Sequence: len(st.History) + 1})
		}
	}
	if len(i.Jobs) == 0 {
		return Invitation{}, fmt.Errorf("no circuits in requested track")
	}
	if err = s.signInvitation(&i); err != nil {
		return Invitation{}, err
	}
	if err = WriteJSON(filepath.Join(s.Dir, "identities", id+".json"), map[string]string{"contributor": name, "track": track}); err != nil {
		return Invitation{}, err
	}
	// Rotation fails closed: old tokens are removed before the new grant is
	// written. An interrupted write can be retried by the stable public ID.
	for _, old := range stored {
		if old.ID == id {
			if err = s.revokeToken(old); err != nil {
				return Invitation{}, err
			}
		}
	}
	return i, WriteJSON(filepath.Join(s.Dir, "invitations", hashBytes([]byte(token))+".json"), i)
}

func (s *Store) invitation(token string) (Invitation, error) {
	var i Invitation
	if !validInvitationID(token) {
		return i, fmt.Errorf("invalid invitation")
	}
	err := ReadJSON(filepath.Join(s.Dir, "invitations", hashBytes([]byte(token))+".json"), &i)
	if err != nil || !s.nowUTC().Before(i.Expires) || i.Token != token || !validInvitationID(i.ID) || i.ManifestSHA256 != s.ManifestArtifact.SHA256 {
		return Invitation{}, fmt.Errorf("invalid or expired invitation")
	}
	if err = s.verifyMessage("invitation", i); err != nil {
		return Invitation{}, err
	}
	return i, nil
}

// revokeToken requires the coordinator lock.
func (s *Store) revokeToken(i Invitation) error {
	owner := hashBytes([]byte(i.Token))
	if err := os.Remove(filepath.Join(s.Dir, "invitations", owner+".json")); err != nil {
		return err
	}
	for _, c := range s.Manifest.Circuits {
		var lease Lease
		if err := ReadJSON(s.leasePath(c.Spec.ID), &lease); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if lease.Owner == owner {
			if err := os.Remove(s.leasePath(c.Spec.ID)); err != nil {
				return err
			}
		}
	}
	return nil
}
