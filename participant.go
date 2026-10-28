package ceremony

import (
	"fmt"
	"net/url"
	"os"
	"strings"
)

// InvitationLink keeps the bearer token out of HTTP request URLs and referrers.
// Deliver the link privately; its manifest pin must be authenticated out of band.
func InvitationLink(i Invitation) (string, error) {
	if err := ValidateURL(i.URL); err != nil {
		return "", err
	}
	if i.Mode != "queue" || i.Track != Core || (Artifact{SHA256: i.Token, Size: 1}).Validate() != nil ||
		(Artifact{SHA256: i.ManifestSHA256, Size: 1}).Validate() != nil {
		return "", fmt.Errorf("browser links require a queued core invitation")
	}
	fragment := url.Values{"token": {i.Token}, "manifest": {i.ManifestSHA256}}
	return strings.TrimRight(i.URL, "/") + "/#" + fragment.Encode(), nil
}

// Release relinquishes only this invitation's reservation. Accepted contributions
// and another participant's reservation are never changed.
func (s *Store) Release(token, id string) error {
	i, err := s.invitation(token)
	if err != nil {
		return err
	}
	allowed := false
	for _, j := range i.Jobs {
		allowed = allowed || j.Circuit.Spec.ID == id
	}
	if i.Mode != "queue" || !allowed || !safeID.MatchString(id) {
		return fmt.Errorf("circuit is not in queued invitation")
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	if _, err = s.invitation(token); err != nil {
		return err
	}
	var l Lease
	if err = ReadJSON(s.leasePath(id), &l); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	if l.Owner != hashBytes([]byte(token)) {
		return nil
	}
	return os.Remove(s.leasePath(id))
}
