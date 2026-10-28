package ceremony

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type githubTransport func(*http.Request) (*http.Response, error)

func (f githubTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type githubFixture struct {
	t      *testing.T
	s      *Store
	g      *githubOAuth
	server *httptest.Server
	i      Invitation
	client *http.Client
	calls  int
	userID int64
	login  string
}

func newGitHubFixture(t *testing.T) *githubFixture {
	t.Helper()
	f := &githubFixture{t: t, s: signedStore(t), userID: 12345, login: "alice-github"}
	f.server = httptest.NewUnstartedServer(nil)
	origin := "http://" + f.server.Listener.Addr().String()
	var err error
	f.g, err = newGitHubOAuth(f.s, origin, GitHubOAuthConfig{ClientID: "public-test-client", ClientSecret: "private-test-app-secret"})
	must(t, err)
	f.g.client.Transport = githubTransport(func(req *http.Request) (*http.Response, error) {
		f.calls++
		var body string
		if req.URL.String() == "https://github.com/login/oauth/access_token" {
			must(t, req.ParseForm())
			if req.Method != "POST" || req.Form.Get("client_secret") != f.g.config.ClientSecret || req.Form.Get("code_verifier") == "" || req.Form.Get("redirect_uri") != origin+"/github/callback" {
				t.Error("OAuth exchange omitted confidential client or PKCE binding")
			}
			body = `{"access_token":"private-test-access-token","token_type":"bearer","scope":""}`
		} else if req.URL.String() == "https://api.github.com/user" {
			if req.Header.Get("Authorization") != "Bearer private-test-access-token" {
				t.Error("profile request did not authenticate token")
			}
			body = fmt.Sprintf(`{"id":%d,"login":%q,"type":"User"}`, f.userID, f.login)
		} else {
			t.Errorf("unexpected provider endpoint %s", req.URL.Host)
			return nil, fmt.Errorf("unexpected endpoint")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	f.server.Config.Handler = f.g.handler(f.s.Handler())
	f.server.Start()
	t.Cleanup(f.server.Close)
	f.i, err = f.s.InviteQueued(origin, "alice", Core, time.Hour)
	must(t, err)
	c := NewClient(t.TempDir())
	c.Progress = io.Discard
	must(t, c.Run(context.Background(), f.i, Core))
	jar, err := cookiejar.New(nil)
	must(t, err)
	f.client = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return f
}

func (f *githubFixture) request(method, path, body, origin string) (int, []byte, http.Header) {
	f.t.Helper()
	req, err := http.NewRequest(method, f.server.URL+path, strings.NewReader(body))
	must(f.t, err)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	response, err := f.client.Do(req)
	must(f.t, err)
	defer response.Body.Close()
	b, err := io.ReadAll(response.Body)
	must(f.t, err)
	return response.StatusCode, b, response.Header
}

func (f *githubFixture) begin() string {
	f.t.Helper()
	link, err := NewClient("").LinkGitHub(context.Background(), f.i)
	must(f.t, err)
	u, err := url.Parse(link)
	must(f.t, err)
	ticket := strings.TrimPrefix(u.Fragment, "github=")
	if strings.Contains(link, f.i.Token) || !validInvitationID(ticket) {
		f.t.Fatal("link leaked invitation or has invalid ticket")
	}
	status, b, headers := f.request("POST", "/v1/github/begin", `{"ticket":"`+ticket+`"}`, f.server.URL)
	if status != 200 || !strings.Contains(headers.Get("Set-Cookie"), "HttpOnly") || !strings.Contains(headers.Get("Set-Cookie"), "SameSite=Lax") {
		f.t.Fatal("missing browser session")
	}
	var result map[string]string
	must(f.t, json.Unmarshal(b, &result))
	u, err = url.Parse(result["url"])
	must(f.t, err)
	q := u.Query()
	if u.Host != "github.com" || q.Get("scope") != "" || q.Get("code_challenge_method") != "S256" || q.Get("redirect_uri") != f.server.URL+"/github/callback" {
		f.t.Fatal("incorrect authorization scope or callback")
	}
	f.g.mu.Lock()
	for _, session := range f.g.sessions {
		if session.State == q.Get("state") {
			digest := sha256.Sum256([]byte(session.Verifier))
			if base64.RawURLEncoding.EncodeToString(digest[:]) != q.Get("code_challenge") {
				f.t.Error("PKCE challenge mismatch")
			}
		}
	}
	f.g.mu.Unlock()
	status, _, _ = f.request("POST", "/v1/github/begin", `{"ticket":"`+ticket+`"}`, f.server.URL)
	if status != 410 {
		f.t.Fatal("one-time link replay succeeded")
	}
	return q.Get("state")
}

func (f *githubFixture) authorize() string {
	f.t.Helper()
	state := f.begin()
	path := "/github/callback?code=private-test-code&state=" + state
	status, _, headers := f.request("GET", path, "", "")
	if status != 303 || headers.Get("Location") != f.server.URL+"/#github=confirm" || f.calls < 2 {
		f.t.Fatal("OAuth callback failed")
	}
	calls := f.calls
	status, _, headers = f.request("GET", path, "", "")
	if status != 303 || headers.Get("Location") != f.server.URL+"/#github=failed" || f.calls != calls {
		f.t.Fatal("OAuth callback replay exchanged code again")
	}
	status, b, _ := f.request("GET", "/v1/github/session", "", "")
	if status != 200 || bytes.Contains(b, []byte(f.i.Token)) || bytes.Contains(b, []byte("private-test-access-token")) {
		f.t.Fatal("invalid or secret-bearing confirmation")
	}
	var session struct {
		Confirm  string `json:"confirm"`
		GitHubID int64  `json:"github_id"`
	}
	must(f.t, json.Unmarshal(b, &session))
	if session.GitHubID != f.userID || !validInvitationID(session.Confirm) {
		f.t.Fatal("wrong confirmation account")
	}
	return session.Confirm
}

func TestGitHubLinkExplicitConsentAndImmutablePublication(t *testing.T) {
	f := newGitHubFixture(t)
	_, before, err := f.s.checkpoint()
	must(t, err)
	confirm := f.authorize()
	record, err := f.s.completedRecord(f.i.ID)
	must(t, err)
	identity, err := f.s.GitHubIdentityFor(context.Background(), record)
	must(t, err)
	if identity != nil {
		t.Fatal("published before explicit confirmation")
	}
	body := `{"confirm":"` + confirm + `"}`
	for _, origin := range []string{"", "https://attacker.example"} {
		status, _, _ := f.request("POST", "/v1/github/confirm", body, origin)
		if status != 403 {
			t.Fatal("cross-origin confirmation accepted")
		}
	}
	status, b, _ := f.request("POST", "/v1/github/confirm", body, f.server.URL)
	if status != 200 {
		t.Fatalf("confirmation failed: %d", status)
	}
	must(t, json.Unmarshal(b, &identity))
	public, err := f.s.PublicSigningKey()
	must(t, err)
	must(t, VerifyGitHubIdentity(*identity, record, public))
	_, after, err := f.s.checkpoint()
	must(t, err)
	if before != after {
		t.Fatal("identity publication changed ceremony history")
	}
	for _, mutate := range []func(*GitHubIdentity){func(x *GitHubIdentity) { x.GitHubID++ }, func(x *GitHubIdentity) { x.InvitationID = f.i.Token }, func(x *GitHubIdentity) { x.ReceiptsSHA256 = f.i.Token }, func(x *GitHubIdentity) { x.GitHubLogin = "mallory" }} {
		changed := *identity
		mutate(&changed)
		if VerifyGitHubIdentity(changed, record, public) == nil {
			t.Fatal("forged identity verified")
		}
	}
	records, err := FetchContributionRecords(context.Background(), f.server.URL, f.s.ManifestArtifact.SHA256)
	must(t, err)
	identities, err := FetchGitHubIdentities(context.Background(), f.server.URL, records)
	must(t, err)
	if len(identities) != 1 {
		t.Fatal("public identity did not verify")
	}
	dir := t.TempDir()
	must(t, SaveGitHubIdentities(dir, identities))
	must(t, SaveGitHubIdentities(dir, identities))
	identities[0].GitHubID++
	if SaveGitHubIdentities(dir, identities) == nil {
		t.Fatal("overwrote archived identity")
	}
	// Numeric ID remains the identity even after a GitHub login rename.
	f.login = "renamed-alice"
	confirm = f.authorize()
	status, b, _ = f.request("POST", "/v1/github/confirm", `{"confirm":"`+confirm+`"}`, f.server.URL)
	if status != 200 || !bytes.Contains(b, []byte(`"github_login":"alice-github"`)) {
		t.Fatal("same account retry changed immutable link")
	}
	f.userID++
	confirm = f.authorize()
	status, _, _ = f.request("POST", "/v1/github/confirm", `{"confirm":"`+confirm+`"}`, f.server.URL)
	if status != 409 {
		t.Fatal("replaced GitHub account")
	}
}

func TestGitHubSessionRejectsCrossBrowserRevocationAndExpiry(t *testing.T) {
	f := newGitHubFixture(t)
	state := f.begin()
	other := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := other.Get(f.server.URL + "/github/callback?code=stolen&state=" + state)
	must(t, err)
	r.Body.Close()
	if !strings.HasSuffix(r.Header.Get("Location"), "#github=failed") || f.calls != 0 {
		t.Fatal("callback accepted from another browser")
	}
	confirm := f.authorize()
	must(t, f.s.RevokeInvitation(f.i.ID))
	status, _, _ := f.request("POST", "/v1/github/confirm", `{"confirm":"`+confirm+`"}`, f.server.URL)
	if status != 401 {
		t.Fatal("revoked invitation linked an account")
	}
	f.i, err = f.s.ReissueInvitation(f.server.URL, f.i.ID, time.Hour)
	must(t, err)
	confirm = f.authorize()
	f.g.mu.Lock()
	for key, session := range f.g.sessions {
		session.Expires = time.Now().Add(-time.Second)
		f.g.sessions[key] = session
	}
	f.g.mu.Unlock()
	status, _, _ = f.request("POST", "/v1/github/confirm", `{"confirm":"`+confirm+`"}`, f.server.URL)
	if status != 410 {
		t.Fatal("expired browser session linked an account")
	}
}

type githubLostPublication struct {
	BlobStore
	failBefore bool
}

func (s githubLostPublication) ConditionalPut(ctx context.Context, key string, ref Artifact, etag string) error {
	if !s.failBefore {
		if err := s.BlobStore.ConditionalPut(ctx, key, ref, etag); err != nil {
			return err
		}
	}
	return fmt.Errorf("simulated publication failure")
}

func TestGitHubSidecarRemoteRecoveryAndPublicationFailure(t *testing.T) {
	f := newGitHubFixture(t)
	archive := testArchive{dir: t.TempDir()}
	f.s.Remote = archive
	checkpoint, err := f.s.Sync(context.Background())
	must(t, err)
	record, err := f.s.completedRecord(f.i.ID)
	must(t, err)
	f.s.Remote = githubLostPublication{BlobStore: archive, failBefore: true}
	if _, err = f.s.saveGitHubIdentity(context.Background(), record, f.userID, f.login); err == nil {
		t.Fatal("reported success before remote publication")
	}
	if _, err = os.Stat(f.s.githubIdentityPath(f.i.ID)); !os.IsNotExist(err) {
		t.Fatal("published local identity before remote commit")
	}
	f.s.Remote = githubLostPublication{BlobStore: archive}
	identity, err := f.s.saveGitHubIdentity(context.Background(), record, f.userID, f.login)
	must(t, err)
	dest := filepath.Join(t.TempDir(), "restored")
	must(t, Restore(context.Background(), archive, dest, f.s.ManifestArtifact, checkpoint))
	restored, err := Open(dest)
	must(t, err)
	restored.Remote = archive
	got, err := restored.GitHubIdentityFor(context.Background(), record)
	must(t, err)
	if got == nil || *got != *identity {
		t.Fatal("sidecar did not recover independently from S3")
	}
	must(t, filepath.WalkDir(archive.dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, secret := range []string{f.i.Token, f.g.config.ClientSecret, "private-test-access-token", "private-test-code"} {
			if bytes.Contains(b, []byte(secret)) {
				t.Error("private material entered public archive")
			}
		}
		return nil
	}))
}

func TestGitHubProviderRejectsPrivateScopesAndRedirects(t *testing.T) {
	f := newGitHubFixture(t)
	for _, body := range []string{`{"access_token":"secret","token_type":"bearer","scope":"repo"}`, `{"access_token":"secret","token_type":"bearer"}`, strings.Repeat("x", 65<<10)} {
		f.g.client.Transport = githubTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
		})
		if _, err := f.g.exchange(context.Background(), "code", "verifier"); err == nil {
			t.Fatal("unsafe provider response accepted")
		}
	}
	redirectCalls := 0
	f.g.client.Transport = githubTransport(func(*http.Request) (*http.Response, error) {
		redirectCalls++
		return &http.Response{StatusCode: 302, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{"Location": []string{"https://attacker.example/token"}}}, nil
	})
	if _, err := f.g.exchange(context.Background(), "code", "verifier"); err == nil || redirectCalls != 1 {
		t.Fatal("followed provider redirect or accepted it")
	}
	path := filepath.Join(t.TempDir(), "oauth.json")
	must(t, os.WriteFile(path, []byte(`{"client_id":"test-client","client_secret":"private-secret-value"}`), 0600))
	_, err := LoadGitHubOAuthConfig(path)
	must(t, err)
	must(t, os.Chmod(path, 0644))
	if _, err := LoadGitHubOAuthConfig(path); err == nil {
		t.Fatal("world-readable OAuth secret accepted")
	}
}

func TestGitHubRejectsIncompleteAndForgedInvitations(t *testing.T) {
	f := newGitHubFixture(t)
	bob, err := f.s.InviteQueued(f.server.URL, "bob", Core, time.Hour)
	must(t, err)
	if _, err = NewClient("").LinkGitHub(context.Background(), bob); err == nil {
		t.Fatal("incomplete contribution received a linking grant")
	}
	forged := f.i
	forged.Contributor = "mallory"
	if _, err = NewClient("").LinkGitHub(context.Background(), forged); err == nil {
		t.Fatal("forged invitation used for linking")
	}
	if f.calls != 0 {
		t.Fatal("invalid invitation contacted GitHub")
	}
}
