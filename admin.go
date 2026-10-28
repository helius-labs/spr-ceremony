package ceremony

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

//go:embed web/admin.html web/admin.js web/style.css
var adminAssets embed.FS

type AdminInvitation struct {
	ID          string    `json:"id"`
	Contributor string    `json:"contributor"`
	Expires     time.Time `json:"expires"`
	Accepted    int       `json:"accepted"`
	Total       int       `json:"total"`
	HasLink     bool      `json:"has_link"`
}

// RevokeInvitation removes private authorization and its reservations. Accepted
// contributions are immutable. This shares the acceptance lock so revocation
// cannot race a queued commit or renewal.
func (s *Store) RevokeInvitation(id string) error {
	if (Artifact{SHA256: id, Size: 1}).Validate() != nil {
		return fmt.Errorf("invalid invitation ID")
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	stored, err := s.storedInvitations()
	if err != nil {
		return err
	}
	for _, i := range stored {
		if i.ID == id {
			if err = s.revokeToken(i); err != nil {
				return err
			}
			log.Printf("admin action=revoke contributor=%s invitation=%s", i.Contributor, i.ID)
			return nil
		}
	}
	return fmt.Errorf("invitation unavailable")
}

func (s *Store) adminStatus() (any, error) {
	unlock, err := s.lock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	cp, ref, err := s.checkpoint()
	if err != nil {
		return nil, err
	}
	byID := map[string]AdminInvitation{}
	total := 0
	for _, c := range s.Manifest.Circuits {
		if c.Spec.Track != Core {
			continue
		}
		total++
		st, e := s.State(c.Spec.ID)
		if e != nil {
			return nil, e
		}
		for _, receipt := range st.History {
			i := byID[receipt.InvitationID]
			i.ID = receipt.InvitationID
			i.Contributor = receipt.Contributor
			i.Accepted++
			byID[i.ID] = i
		}
	}
	stored, err := s.storedInvitations()
	if err != nil {
		return nil, err
	}
	for _, grant := range stored {
		if grant.Track == Core {
			i := byID[grant.ID]
			i.ID = grant.ID
			i.Contributor = grant.Contributor
			i.Expires = grant.Expires
			i.HasLink = true
			byID[i.ID] = i
		}
	}
	invitations := []AdminInvitation{}
	for _, i := range byID {
		i.Total = total
		invitations = append(invitations, i)
	}
	sort.Slice(invitations, func(i, j int) bool {
		if invitations[i].Contributor == invitations[j].Contributor {
			return invitations[i].ID < invitations[j].ID
		}
		return invitations[i].Contributor < invitations[j].Contributor
	})
	reservations := []map[string]any{}
	for _, c := range s.Manifest.Circuits {
		var lease Lease
		if err = ReadJSON(s.leasePath(c.Spec.ID), &lease); os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if s.nowUTC().Before(lease.Expires) {
			reservations = append(reservations, map[string]any{"circuit": c.Spec.ID, "expires": lease.Expires})
		}
	}
	return map[string]any{"phase": cp.Lifecycle.Phase, "beacon": cp.Lifecycle.Beacon, "manifest": s.ManifestArtifact, "checkpoint": ref, "invitations": invitations, "reservations": reservations, "insecure_test_setup": s.Manifest.Phase1.InsecureTestSetup, "s3": s.Remote != nil, "coordinator_key": s.Manifest.CoordinatorKey, "beacon_recoveries": cp.Lifecycle.Recoveries}, nil
}

// AdminHandler is only for a separate loopback listener reached through SSM.
// IAM authenticates the tunnel. Host, peer, Origin and custom-header checks
// reject DNS rebinding and requests initiated by an unrelated web page.
// Never mount this handler on the public listener or reverse proxy it.
func (s *Store) AdminHandler(origin, publicURL string) (http.Handler, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("admin origin must be an exact loopback HTTP origin")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() || u.Port() == "" {
		return nil, fmt.Errorf("admin requires a numeric loopback address and port")
	}
	if err = ValidateURL(publicURL); err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	for route, asset := range map[string]string{"/{$}": "web/admin.html", "/admin.js": "web/admin.js", "/style.css": "web/style.css"} {
		body, e := adminAssets.ReadFile(asset)
		if e != nil {
			return nil, e
		}
		mux.HandleFunc("GET "+route, func(w http.ResponseWriter, r *http.Request) {
			http.ServeContent(w, r, asset, time.Time{}, bytes.NewReader(body))
		})
	}
	mux.HandleFunc("GET /admin/status", func(w http.ResponseWriter, r *http.Request) {
		status, e := s.adminStatus()
		if e != nil {
			http.Error(w, "Status unavailable. Check the coordinator.", 503)
			return
		}
		respond(w, status)
	})
	mux.HandleFunc("POST /admin/invite", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Name  string `json:"name"`
			Hours int    `json:"hours"`
		}
		if !adminJSON(w, r, &input) {
			return
		}
		if !safeID.MatchString(input.Name) || input.Hours < 1 || input.Hours > 168 {
			http.Error(w, "Use a pseudonym and a lifetime of 1–168 hours.", 400)
			return
		}
		i, e := s.InviteQueued(publicURL, input.Name, Core, time.Duration(input.Hours)*time.Hour)
		if e != nil {
			http.Error(w, "Cannot invite. Use a unique active pseudonym and check the ceremony deadline.", 409)
			return
		}
		link, e := InvitationLink(i)
		if e != nil {
			http.Error(w, "Cannot create invitation link.", 500)
			return
		}
		log.Printf("admin action=invite contributor=%s invitation=%s", i.Contributor, i.ID)
		respond(w, map[string]any{"link": link, "invitation": i})
	})
	mux.HandleFunc("POST /admin/reissue", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ID    string `json:"id"`
			Hours int    `json:"hours"`
		}
		if !adminJSON(w, r, &input) {
			return
		}
		if !validInvitationID(input.ID) || input.Hours < 1 || input.Hours > 168 {
			http.Error(w, "Invalid invitation ID or lifetime.", 400)
			return
		}
		i, e := s.ReissueInvitation(publicURL, input.ID, time.Duration(input.Hours)*time.Hour)
		if e != nil {
			http.Error(w, "Cannot reissue. Check the ceremony deadline and active pseudonyms.", 409)
			return
		}
		link, e := InvitationLink(i)
		if e != nil {
			http.Error(w, "Cannot create invitation link.", 500)
			return
		}
		log.Printf("admin action=reissue contributor=%s invitation=%s", i.Contributor, i.ID)
		respond(w, map[string]any{"link": link, "invitation": i})
	})
	mux.HandleFunc("POST /admin/revoke", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			ID string `json:"id"`
		}
		if !adminJSON(w, r, &input) {
			return
		}
		if e := s.RevokeInvitation(input.ID); e != nil {
			http.Error(w, "Cannot revoke invitation.", 409)
			return
		}
		respond(w, map[string]bool{"revoked": true})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		host, _, e := net.SplitHostPort(r.RemoteAddr)
		peer := net.ParseIP(host)
		if e != nil || peer == nil || !peer.IsLoopback() || r.Host != u.Host || r.URL.RawQuery != "" {
			http.Error(w, "Forbidden", 403)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != origin {
			http.Error(w, "Forbidden", 403)
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			http.Error(w, "Forbidden", 403)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/admin/") {
			if r.Header.Get("X-SPR-Admin") != "1" || (r.Method != "GET" && r.Header.Get("Origin") != origin) {
				http.Error(w, "Forbidden", 403)
				return
			}
		}
		mux.ServeHTTP(w, r)
	}), nil
}

func adminJSON(w http.ResponseWriter, r *http.Request, value any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "JSON required", 415)
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		http.Error(w, "Invalid request", 400)
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		http.Error(w, "Invalid request", 400)
		return false
	}
	return true
}
