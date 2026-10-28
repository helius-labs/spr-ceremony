package ceremony

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPrivateAdminBoundaryAndRevocation(t *testing.T) {
	s := demoStore(t)
	const origin = "http://127.0.0.1:8081"
	handler, err := s.AdminHandler(origin, "https://ceremony.example")
	must(t, err)
	call := func(method, path, body, host, peer, requestOrigin, header, site string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, origin+path, strings.NewReader(body))
		r.Host, r.RemoteAddr = host, peer
		r.Header.Set("Origin", requestOrigin)
		r.Header.Set("X-SPR-Admin", header)
		r.Header.Set("Sec-Fetch-Site", site)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct{ host, peer, origin, header, site string }{
		{"evil.example", "127.0.0.1:1", origin, "1", ""},
		{"127.0.0.1:8081", "10.0.0.1:1", origin, "1", ""},
		{"127.0.0.1:8081", "127.0.0.1:1", "https://evil.example", "1", ""},
		{"127.0.0.1:8081", "127.0.0.1:1", "", "1", ""},
		{"127.0.0.1:8081", "127.0.0.1:1", origin, "", ""},
		{"127.0.0.1:8081", "127.0.0.1:1", origin, "1", "cross-site"},
	} {
		w := call("POST", "/admin/invite", `{"name":"blocked","hours":24}`, tc.host, tc.peer, tc.origin, tc.header, tc.site)
		if w.Code != 403 {
			t.Fatalf("admin boundary failed: %d", w.Code)
		}
	}
	request := func(method, path, body string) *httptest.ResponseRecorder {
		return call(method, path, body, "127.0.0.1:8081", "127.0.0.1:1", origin, "1", "same-origin")
	}
	for _, body := range []string{`{"name":"bad/name","hours":24}`, `{"name":"alice","hours":999}`, `{"name":"alice","hours":24,"extra":"secret"}`, `{"name":"alice","hours":24} {}`} {
		if w := request("POST", "/admin/invite", body); w.Code != 400 {
			t.Fatalf("bad input: %d", w.Code)
		}
	}
	w := request("POST", "/admin/invite", `{"name":"alice","hours":24}`)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("invite failed: %d", w.Code)
	}
	var created struct {
		Link       string     `json:"link"`
		Invitation Invitation `json:"invitation"`
	}
	must(t, json.Unmarshal(w.Body.Bytes(), &created))
	if !strings.Contains(created.Link, "#") {
		t.Fatal("missing private fragment")
	}
	claim, err := s.Claim(created.Invitation.Token, time.Minute)
	must(t, err)
	w = request("GET", "/admin/status", "")
	if w.Code != 200 || strings.Contains(w.Body.String(), created.Invitation.Token) || strings.Contains(w.Body.String(), created.Link) {
		t.Fatal("status leaked private invitation")
	}
	w = request("POST", "/admin/revoke", `{"id":"`+hashBytes([]byte(created.Invitation.Token))+`"}`)
	if w.Code != 200 {
		t.Fatalf("revoke: %d", w.Code)
	}
	if _, err = s.Claim(created.Invitation.Token, time.Minute); err == nil {
		t.Fatal("revoked invitation claimed work")
	}
	if _, err = s.Renew(created.Invitation.Token, claim.Job.Circuit.Spec.ID, time.Minute); err == nil {
		t.Fatal("revoked invitation renewed work")
	}
	next, err := s.InviteQueued("https://ceremony.example", "bob", Core, time.Hour)
	must(t, err)
	available, err := s.Claim(next.Token, time.Minute)
	must(t, err)
	if available.Job.Circuit.Spec.ID != claim.Job.Circuit.Spec.ID {
		t.Fatal("revocation did not release reservation")
	}
	for _, bad := range []string{"http://0.0.0.0:8081", "http://localhost:8081", "http://127.0.0.1:8081/path", "https://127.0.0.1:8081"} {
		if _, err = s.AdminHandler(bad, "https://ceremony.example"); err == nil {
			t.Fatal("unsafe admin listener")
		}
	}
	public := s.Handler()
	for _, path := range []string{"/admin/status", "/admin/invite", "/admin.js"} {
		w = httptest.NewRecorder()
		public.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != 404 {
			t.Fatal("admin route on public handler")
		}
	}
}

func TestPublicViewDoesNotExposeInvitationsOrProvisionalKeys(t *testing.T) {
	s := demoStore(t)
	i, err := s.InviteQueued("https://ceremony.example", "private_pseudonym", Core, time.Hour)
	must(t, err)
	view, err := s.PublicView()
	must(t, err)
	body, err := json.Marshal(view)
	must(t, err)
	if strings.Contains(string(body), i.Token) || strings.Contains(string(body), i.Contributor) {
		t.Fatal("private invitation leaked")
	}
	if view.FinalExport != nil || len(view.Circuits) != 2 || view.Phase != "open" {
		t.Fatal("incorrect public phase")
	}
	for _, c := range view.Circuits {
		if c.PK != nil || c.VK != nil {
			t.Fatal("provisional keys exposed as final")
		}
	}
}
