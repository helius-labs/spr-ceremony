package ceremony

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPublicRateLimitCannotBeBypassedWithForwardedHeaders(t *testing.T) {
	for _, trust := range []bool{false, true} {
		handler := limitWeb(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }), trust)
		limited := false
		for n := range 500 {
			r := httptest.NewRequest("GET", "https://ceremony.example/", nil)
			r.RemoteAddr = "192.0.2.1:1"
			r.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.%d.%d", n/250, n%250))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code == 429 {
				limited = true
				break
			}
		}
		if !limited {
			t.Fatal("direct peer bypassed limits with an untrusted forwarded header")
		}
		r := httptest.NewRequest("GET", "https://ceremony.example/", nil)
		r.RemoteAddr = "192.0.2.2:1"
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 204 {
			t.Fatal("rate limit leaked to another peer")
		}
	}
}

func TestReleaseInstallerAndNativeAssetsArePinned(t *testing.T) {
	web, build := testBrowserBundle(t)
	dir := t.TempDir()
	hashes := map[string]string{}
	for _, platform := range []string{"darwin-arm64", "darwin-amd64", "linux-arm64", "linux-amd64"} {
		name := "spr-ceremony-" + platform
		body := []byte(name)
		must(t, os.WriteFile(filepath.Join(dir, name), body, 0600))
		hashes[name] = hashBytes(body)
	}
	installer := []byte("#!/bin/bash\n# pinned installer\n")
	must(t, os.WriteFile(filepath.Join(dir, "join.sh"), installer, 0600))
	metadata := map[string]any{"source_revision": build.SourceRevision, "go": "test", "sha256": hashes, "version": "v0.1.0-rc.1", "installer": Artifact{SHA256: hashBytes(installer), Size: int64(len(installer))}}
	must(t, WriteJSON(filepath.Join(dir, "build.json"), metadata))
	options := WebOptions{Directory: web, NativeDirectory: dir, Origin: "https://ceremony.example"}
	handler, err := WebHandler(http.NotFoundHandler(), options)
	must(t, err)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", options.Origin+"/join.sh", nil))
	if w.Code != 200 || w.Body.String() != string(installer) {
		t.Fatal("wrong installer served")
	}
	must(t, os.WriteFile(filepath.Join(dir, "join.sh"), []byte("tampered"), 0600))
	if _, err = WebHandler(http.NotFoundHandler(), options); err == nil {
		t.Fatal("changed installer accepted")
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", options.Origin+"/join.sh", nil))
	if w.Body.String() != string(installer) {
		t.Fatal("running host served changed installer")
	}
}

func testBrowserBundle(t *testing.T) (string, BrowserBuild) {
	t.Helper()
	dir := t.TempDir()
	build := BrowserBuild{Format: "spr-browser-build-v1", Engine: Engine, SourceRevision: strings.Repeat("a", 40), GoVersion: "test", Assets: map[string]Artifact{}}
	for _, name := range BrowserAssets {
		body := []byte("asset:" + name)
		path := filepath.Join(dir, name)
		must(t, os.WriteFile(path, body, 0600))
		build.Assets[name] = Artifact{SHA256: hashBytes(body), Size: int64(len(body))}
	}
	must(t, WriteJSON(filepath.Join(dir, "build.json"), build))
	return dir, build
}

func TestHostedAssetsAndOriginBoundary(t *testing.T) {
	dir, _ := testBrowserBundle(t)
	apiCalls := 0
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { apiCalls++; w.WriteHeader(204) })
	handler, err := WebHandler(api, WebOptions{Directory: dir, Origin: "https://ceremony.example", DownloadOrigin: "https://objects.example"})
	must(t, err)
	// Assets are authenticated at startup and cannot change under a live process.
	must(t, os.WriteFile(filepath.Join(dir, "hosted.html"), []byte("changed after startup"), 0600))
	for _, tc := range []struct {
		path, host, origin, site string
		want                     int
	}{
		{"/", "ceremony.example", "", "", 200},
		{"/worker.js", "ceremony.example", "", "", 200},
		{"/build.json", "ceremony.example", "", "", 200},
		{"/ui-config.json", "ceremony.example", "", "", 200},
		{"/v1/checkpoint", "ceremony.example", "https://ceremony.example", "same-origin", 204},
		{"/v1/checkpoint", "evil.example", "", "", 421},
		{"/v1/checkpoint", "ceremony.example", "https://evil.example", "", 403},
		{"/v1/checkpoint", "ceremony.example", "null", "", 403},
		{"/v1/checkpoint", "ceremony.example", "", "cross-site", 403},
		{"/invitations/private.json", "ceremony.example", "", "", 404},
		{"/s3.json", "ceremony.example", "", "", 404},
		{"/app.js", "ceremony.example", "", "", 404},
		{"/fixture.json", "ceremony.example", "", "", 404},
	} {
		r := httptest.NewRequest("GET", "https://"+tc.host+tc.path, nil)
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("Sec-Fetch-Site", tc.site)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s %s %s: %d %s", tc.path, tc.host, tc.origin, w.Code, w.Body.String())
		}
		if tc.path == "/" && w.Code == 200 && w.Body.String() != "asset:hosted.html" {
			t.Fatal("live assets changed")
		}
		csp := w.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "frame-ancestors 'none'") || strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "'unsafe-eval'") {
			t.Fatal("unsafe CSP", csp)
		}
		if tc.path == "/worker.js" && strings.Contains(csp, "objects.example") {
			t.Fatal("worker may access external downloads")
		}
		if w.Header().Get("Referrer-Policy") != "no-referrer" || w.Header().Get("Strict-Transport-Security") == "" {
			t.Fatal("missing security headers")
		}
	}
	if apiCalls != 1 {
		t.Fatal("cross-origin request reached API", apiCalls)
	}
	if _, err = WebHandler(api, WebOptions{Directory: dir, Origin: "https://ceremony.example"}); err == nil {
		t.Fatal("changed asset was accepted at startup")
	}
}

func TestHostedRejectsUnpinnedOrUnsafeBuild(t *testing.T) {
	dir, build := testBrowserBundle(t)
	build.SourceModified = true
	must(t, WriteJSON(filepath.Join(dir, "build.json"), build))
	options := WebOptions{Directory: dir, Origin: "https://ceremony.example"}
	if _, err := WebHandler(http.NotFoundHandler(), options); err == nil {
		t.Fatal("uncommitted production build accepted")
	}
	options.InsecureTestSetup = true
	_, err := WebHandler(http.NotFoundHandler(), options)
	must(t, err)
	for _, origin := range []string{"http://ceremony.example", "https://example/a", "https://example?x=1", "https://example/#x", "https://a:b@example"} {
		options.Origin = origin
		if _, err := WebHandler(http.NotFoundHandler(), options); err == nil {
			t.Fatalf("unsafe origin: %s", origin)
		}
	}
	options.Origin = "https://ceremony.example"
	options.DownloadOrigin = "https://objects.example; script-src *"
	if _, err := WebHandler(http.NotFoundHandler(), options); err == nil {
		t.Fatal("CSP injection accepted")
	}
}

func TestBrowserInvitationAndRelease(t *testing.T) {
	s := demoStore(t)
	invite, err := s.InviteQueued("https://ceremony.example", "browser", Core, time.Hour)
	must(t, err)
	other, err := s.InviteQueued("https://ceremony.example", "other", Core, time.Hour)
	must(t, err)
	link, err := InvitationLink(invite)
	must(t, err)
	u, err := url.Parse(link)
	must(t, err)
	values, err := url.ParseQuery(u.Fragment)
	must(t, err)
	if u.RawQuery != "" || values.Get("token") != invite.Token || values.Get("manifest") != invite.ManifestSHA256 {
		t.Fatal("invitation pin or token absent from fragment")
	}
	handler := s.Handler()
	for _, token := range []string{"", "invalid", invite.Token} {
		request := httptest.NewRequest("GET", "/v1/invitation", nil)
		request.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, request)
		if token != invite.Token {
			if w.Code != 401 || strings.Contains(w.Body.String(), invite.Token) {
				t.Fatal("invitation exposed without authentication")
			}
		} else {
			var got Invitation
			must(t, json.Unmarshal(w.Body.Bytes(), &got))
			if w.Code != 200 || got.Token != invite.Token || w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("private invitation not safely returned")
			}
		}
	}
	claim, err := s.Claim(invite.Token, time.Minute)
	must(t, err)
	must(t, s.Release(other.Token, claim.Job.Circuit.Spec.ID))
	_, err = s.lease(invite.Token, claim.Job.Circuit.Spec.ID)
	must(t, err)
	request := httptest.NewRequest("POST", "/v1/release/"+claim.Job.Circuit.Spec.ID, nil)
	request.Header.Set("Authorization", "Bearer "+invite.Token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, request)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, err = s.lease(invite.Token, claim.Job.Circuit.Spec.ID); !os.IsNotExist(err) {
		t.Fatal("reservation not released", err)
	}
	if err = s.Release(invite.Token, "demo_large_track"); err == nil {
		t.Fatal("released uninvited track")
	}
	claim2, err := s.Claim(other.Token, time.Minute)
	must(t, err)
	if claim2.Job.Circuit.Spec.ID != claim.Job.Circuit.Spec.ID {
		t.Fatal("released circuit not immediately available")
	}
}
