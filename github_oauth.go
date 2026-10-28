package ceremony

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// GitHubOAuthConfig belongs in a private 0600 file, never in a release bundle.
type GitHubOAuthConfig struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

func LoadGitHubOAuthConfig(path string) (GitHubOAuthConfig, error) {
	var config GitHubOAuthConfig
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > 4096 {
		return config, fmt.Errorf("GitHub OAuth config must be a private regular file (0600)")
	}
	if err = ReadJSON(path, &config); err != nil || len(config.ClientID) < 5 || len(config.ClientID) > 128 || len(config.ClientSecret) < 16 || len(config.ClientSecret) > 256 || strings.ContainsAny(config.ClientID+config.ClientSecret, "\r\n\t ") {
		return GitHubOAuthConfig{}, fmt.Errorf("invalid GitHub OAuth config")
	}
	return config, nil
}

type githubUser struct {
	ID    int64  `json:"id"`
	Login string `json:"login"`
	Type  string `json:"type"`
}

type githubSession struct {
	Invitation Invitation
	Record     ContributionRecord
	Expires    time.Time
	State      string
	Verifier   string
	Confirm    string
	User       *githubUser
}

type githubOAuth struct {
	store    *Store
	origin   string
	config   GitHubOAuthConfig
	client   *http.Client
	mu       sync.Mutex
	tickets  map[string]githubSession
	sessions map[string]githubSession
}

// GitHubHandler adds an optional post-contribution OAuth flow. Pending grants
// live only in bounded memory; restart means rerunning link-github. No provider
// token, OAuth code or invitation token is written to public storage or logs.
func (s *Store) GitHubHandler(api http.Handler, origin string, config GitHubOAuthConfig) (http.Handler, error) {
	g, err := newGitHubOAuth(s, origin, config)
	if err != nil {
		return nil, err
	}
	return g.handler(api), nil
}

func newGitHubOAuth(s *Store, origin string, config GitHubOAuthConfig) (*githubOAuth, error) {
	if ValidateURL(origin) != nil || strings.TrimRight(origin, "/") != origin || config.ClientID == "" || config.ClientSecret == "" {
		return nil, fmt.Errorf("GitHub linking requires an exact public origin and OAuth config")
	}
	if err := s.RequireSigningKey(); err != nil || s.Manifest.CoordinatorKey == nil {
		return nil, fmt.Errorf("GitHub linking requires a manifest-bound coordinator key")
	}
	g := &githubOAuth{store: s, origin: origin, config: config, client: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, tickets: map[string]githubSession{}, sessions: map[string]githubSession{}}
	return g, nil
}

func (g *githubOAuth) handler(api http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/github/link", g.link)
	mux.HandleFunc("POST /v1/github/begin", g.begin)
	mux.HandleFunc("GET /github/callback", g.callback)
	mux.HandleFunc("GET /v1/github/session", g.session)
	mux.HandleFunc("POST /v1/github/confirm", g.confirm)
	mux.Handle("/", api)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/github/") || r.URL.Path == "/github/callback" {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			if r.Method == "POST" && r.URL.Path != "/v1/github/link" && r.Header.Get("Origin") != g.origin {
				http.Error(w, "same-origin confirmation required", 403)
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func (g *githubOAuth) cleanLocked() {
	for key, session := range g.tickets {
		if !g.store.nowUTC().Before(session.Expires) {
			delete(g.tickets, key)
		}
	}
	for key, session := range g.sessions {
		if !g.store.nowUTC().Before(session.Expires) {
			delete(g.sessions, key)
		}
	}
}

func (g *githubOAuth) link(w http.ResponseWriter, r *http.Request) {
	i, err := g.store.invitation(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if err != nil || i.Track != Core || strings.TrimRight(i.URL, "/") != g.origin {
		http.Error(w, "valid core invitation required", 401)
		return
	}
	record, err := g.store.completedRecord(i.ID)
	if err != nil {
		http.Error(w, "complete your contribution first", 409)
		return
	}
	ticket, err := randomIdentity()
	if err != nil {
		http.Error(w, "link unavailable", 503)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cleanLocked()
	if len(g.tickets)+len(g.sessions) >= 256 {
		http.Error(w, "linking busy; retry later", 429)
		return
	}
	expires := g.store.nowUTC().Add(10 * time.Minute)
	if i.Expires.Before(expires) {
		expires = i.Expires
	}
	// The bearer is needed only to recheck revocation at confirmation. This
	// private session is never encoded into a response or saved on disk.
	g.tickets[hashBytes([]byte(ticket))] = githubSession{Invitation: i, Record: record, Expires: expires}
	respond(w, map[string]any{"url": g.origin + "/#github=" + ticket, "expires": expires})
}

func githubBody(r *http.Request, target any) error {
	if r.Header.Get("Content-Type") != "application/json" {
		return fmt.Errorf("JSON required")
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 4097))
	if err != nil || len(b) > 4096 {
		return fmt.Errorf("invalid request body")
	}
	return decodeJSON(strings.NewReader(string(b)), target)
}

func (g *githubOAuth) cookieName() string {
	if strings.HasPrefix(g.origin, "https://") {
		return "__Host-spr-github"
	}
	return "spr-github-local"
}

func (g *githubOAuth) begin(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Ticket string `json:"ticket"`
	}
	if githubBody(r, &input) != nil || !validInvitationID(input.Ticket) {
		http.Error(w, "invalid linking request", 400)
		return
	}
	id, err := randomIdentity()
	if err != nil {
		http.Error(w, "link unavailable", 503)
		return
	}
	state, err := randomIdentity()
	if err != nil {
		http.Error(w, "link unavailable", 503)
		return
	}
	verifier, err := randomIdentity()
	if err != nil {
		http.Error(w, "link unavailable", 503)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cleanLocked()
	key := hashBytes([]byte(input.Ticket))
	session, ok := g.tickets[key]
	if !ok {
		http.Error(w, "link expired or already used; rerun link-github", 410)
		return
	}
	delete(g.tickets, key)
	session.State, session.Verifier = state, verifier
	g.sessions[hashBytes([]byte(id))] = session
	http.SetCookie(w, &http.Cookie{Name: g.cookieName(), Value: id, Path: "/", Secure: strings.HasPrefix(g.origin, "https://"), HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 600})
	challenge := sha256.Sum256([]byte(verifier))
	query := url.Values{"client_id": {g.config.ClientID}, "redirect_uri": {g.origin + "/github/callback"}, "state": {state}, "scope": {""}, "code_challenge": {base64.RawURLEncoding.EncodeToString(challenge[:])}, "code_challenge_method": {"S256"}}
	respond(w, map[string]string{"url": "https://github.com/login/oauth/authorize?" + query.Encode()})
}

func (g *githubOAuth) current(r *http.Request) (string, githubSession, bool) {
	cookie, err := r.Cookie(g.cookieName())
	if err != nil || !validInvitationID(cookie.Value) {
		return "", githubSession{}, false
	}
	key := hashBytes([]byte(cookie.Value))
	g.cleanLocked()
	session, ok := g.sessions[key]
	return key, session, ok
}

func (g *githubOAuth) callback(w http.ResponseWriter, r *http.Request) {
	// Atomically consume state before exchanging a code; retries cannot replay
	// a callback. OAuth state is also bound to this browser's HttpOnly cookie.
	g.mu.Lock()
	key, session, ok := g.current(r)
	query := r.URL.Query()
	if !ok || session.State == "" || len(query["state"]) != 1 || query.Get("state") != session.State || len(query["code"]) != 1 || len(query.Get("code")) > 512 || query.Get("code") == "" || query.Get("error") != "" {
		g.mu.Unlock()
		http.Redirect(w, r, g.origin+"/#github=failed", http.StatusSeeOther)
		return
	}
	session.State = ""
	g.sessions[key] = session
	g.mu.Unlock()
	user, err := g.exchange(r.Context(), query.Get("code"), session.Verifier)
	confirm, randomErr := randomIdentity()
	if err != nil || randomErr != nil {
		g.mu.Lock()
		delete(g.sessions, key)
		g.mu.Unlock()
		http.Redirect(w, r, g.origin+"/#github=failed", http.StatusSeeOther)
		return
	}
	g.mu.Lock()
	session.User, session.Verifier, session.Confirm = &user, "", confirm
	g.sessions[key] = session
	g.mu.Unlock()
	http.Redirect(w, r, g.origin+"/#github=confirm", http.StatusSeeOther)
}

func (g *githubOAuth) exchange(ctx context.Context, code, verifier string) (githubUser, error) {
	var user githubUser
	data := url.Values{"client_id": {g.config.ClientID}, "client_secret": {g.config.ClientSecret}, "code": {code}, "redirect_uri": {g.origin + "/github/callback"}, "code_verifier": {verifier}}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://github.com/login/oauth/access_token", strings.NewReader(data.Encode()))
	if err != nil {
		return user, fmt.Errorf("GitHub authorization failed")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var token struct {
		AccessToken string  `json:"access_token"`
		TokenType   string  `json:"token_type"`
		Scope       *string `json:"scope"`
	}
	if err = g.providerJSON(req, &token); err != nil || token.AccessToken == "" || len(token.AccessToken) > 4096 || !strings.EqualFold(token.TokenType, "bearer") || token.Scope == nil || *token.Scope != "" {
		return user, fmt.Errorf("GitHub authorization failed; use a dedicated app without private scopes")
	}
	req, err = http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	if err != nil {
		return user, fmt.Errorf("GitHub identity lookup failed")
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "spr-ceremony")
	if err = g.providerJSON(req, &user); err != nil || user.ID <= 0 || !githubLogin.MatchString(user.Login) || user.Type != "User" {
		return githubUser{}, fmt.Errorf("GitHub identity lookup failed")
	}
	return user, nil
}

func (g *githubOAuth) providerJSON(req *http.Request, result any) error {
	response, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub request failed")
	}
	defer response.Body.Close()
	b, err := io.ReadAll(io.LimitReader(response.Body, 64<<10+1))
	if err != nil || response.StatusCode != 200 || len(b) > 64<<10 || json.Unmarshal(b, result) != nil {
		return fmt.Errorf("invalid GitHub response")
	}
	return nil
}

func (g *githubOAuth) session(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, session, ok := g.current(r)
	if !ok || session.User == nil {
		http.Error(w, "link session expired; rerun link-github", 410)
		return
	}
	respond(w, map[string]any{"github_id": session.User.ID, "github_login": session.User.Login, "contributor": session.Record.Contributor, "total": session.Record.Total, "confirm": session.Confirm})
}

func (g *githubOAuth) confirm(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Confirm string `json:"confirm"`
	}
	if githubBody(r, &input) != nil || !validInvitationID(input.Confirm) {
		http.Error(w, "invalid confirmation", 400)
		return
	}
	g.mu.Lock()
	key, session, ok := g.current(r)
	if !ok || session.User == nil || session.Confirm != input.Confirm {
		g.mu.Unlock()
		http.Error(w, "link session expired; rerun link-github", 410)
		return
	}
	// Only one confirmation may publish. On a storage failure the user can
	// begin again; retries for the same numeric GitHub ID are idempotent.
	delete(g.sessions, key)
	g.mu.Unlock()
	i, err := g.store.invitation(session.Invitation.Token)
	if err != nil || i.ID != session.Record.InvitationID {
		http.Error(w, "invitation expired or revoked", 401)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	identity, err := g.store.saveGitHubIdentity(ctx, session.Record, session.User.ID, session.User.Login)
	if err != nil {
		http.Error(w, "could not link account; retry with the original account or contact the operator", 409)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: g.cookieName(), Path: "/", MaxAge: -1, Secure: strings.HasPrefix(g.origin, "https://"), HttpOnly: true, SameSite: http.SameSiteLaxMode})
	respond(w, identity)
}
