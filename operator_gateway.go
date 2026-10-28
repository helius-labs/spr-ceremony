package ceremony

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const browserSessionCookie = "__Host-SPROperator"
const browserPairingCookie = "__Host-SPRPairing"
const browserSessionTTL = 30 * time.Minute

type operatorPairing struct {
	hash, actor string
	expires     time.Time
}
type operatorSessions struct {
	sync.Mutex
	pairs    map[string]operatorPairing
	sessions map[string]time.Time
	starts   map[string]time.Time
	now      func() time.Time
}

func newOperatorSessions() *operatorSessions {
	return &operatorSessions{pairs: map[string]operatorPairing{}, sessions: map[string]time.Time{}, starts: map[string]time.Time{}, now: time.Now}
}

func (s *operatorSessions) prune() {
	now := s.now()
	for code, p := range s.pairs {
		if !now.Before(p.expires) {
			delete(s.pairs, code)
		}
	}
	for hash, expires := range s.sessions {
		if !now.Before(expires) {
			delete(s.sessions, hash)
		}
	}
	for ip, expires := range s.starts {
		if !now.Before(expires) {
			delete(s.starts, ip)
		}
	}
}

func operatorCookie(w http.ResponseWriter, name, value string, lifetime time.Duration) {
	maxAge := int(lifetime / time.Second)
	if lifetime <= 0 {
		maxAge = -1
	}
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}
func cookieHash(r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil || !validInvitationID(cookie.Value) {
		return ""
	}
	return hashBytes([]byte(cookie.Value))
}
func randomBrowserToken() (string, error) {
	var bytes [32]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes[:]), nil
}
func pairingCode() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	for n := range bytes {
		bytes[n] = alphabet[int(bytes[n])%len(alphabet)]
	}
	return string(bytes[:4]) + "-" + string(bytes[4:8]) + "-" + string(bytes[8:]), nil
}

// AdminHandlerWithGateway adds an authenticated operator gateway without exposing
// the raw loopback API. Only the dedicated VPC Lambda proxy may reach the gateway
// listener; it strips caller identity headers and supplies API Gateway's verified
// IAM principal. Public browser requests must also present an approved cookie.
func (s *Store) AdminHandlerWithGateway(controlOrigin, publicURL, gatewayOrigin, roleARN string) (http.Handler, error) {
	return s.AdminHandlerWithGatewayOptions(controlOrigin, publicURL, gatewayOrigin, roleARN, AuditOptions{Workers: 1})
}

// AdminHandlerWithGatewayOptions applies the server's resource budget to finalization.
func (s *Store) AdminHandlerWithGatewayOptions(controlOrigin, publicURL, gatewayOrigin, roleARN string, options AuditOptions) (http.Handler, error) {
	if err := options.validate(); err != nil {
		return nil, err
	}
	private, err := s.AdminHandler(controlOrigin, publicURL)
	if err != nil {
		return nil, err
	}
	if gatewayOrigin == "" {
		if roleARN != "" {
			return nil, fmt.Errorf("operator role requires a gateway origin")
		}
		return private, nil
	}
	if _, err = operatorRegion(gatewayOrigin); err != nil {
		return nil, err
	}
	role := regexp.MustCompile(`^arn:aws:iam::([0-9]{12}):role/(?:[A-Za-z0-9_+=,.@/-]+/)?([A-Za-z0-9_+=,.@-]+)$`).FindStringSubmatch(roleARN)
	if role == nil {
		return nil, fmt.Errorf("operator gateway requires an explicit IAM role ARN")
	}
	principalPrefix := "arn:aws:sts::" + role[1] + ":assumed-role/" + role[2] + "/"
	gateway, _ := url.Parse(gatewayOrigin)
	control, _ := url.Parse(controlOrigin)
	sessions := newOperatorSessions()
	jobs := newOperatorJobs(s)
	jobs.auditOptions = options
	delegate := func(w http.ResponseWriter, r *http.Request, path string) {
		copy := r.Clone(r.Context())
		u := *r.URL
		u.Path, u.RawPath = path, ""
		copy.URL = &u
		copy.Host = control.Host
		copy.Header.Set("Origin", controlOrigin)
		copy.Header.Set("Sec-Fetch-Site", "same-origin")
		copy.Header.Set("X-SPR-Admin", "1")
		private.ServeHTTP(w, copy)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != gateway.Host {
			private.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		peer := net.ParseIP(host)
		if err != nil || peer == nil || !peer.IsLoopback() || r.URL.RawQuery != "" {
			http.Error(w, "Forbidden", 403)
			return
		}
		path := r.URL.Path
		if strings.HasPrefix(path, "/operator/") {
			actor := r.Header.Get("X-SPR-IAM-Principal")
			if !strings.HasPrefix(actor, principalPrefix) || len(actor) <= len(principalPrefix) || strings.Contains(actor[len(principalPrefix):], "/") {
				http.Error(w, "AWS operator identity required", 403)
				return
			}
			if r.Method == "POST" && r.Header.Get("X-SPR-Manifest") != s.ManifestArtifact.SHA256 {
				http.Error(w, "Campaign changed. Reconnect before continuing.", 409)
				return
			}
			switch path {
			case "/operator/config":
				if r.Method != "GET" {
					http.Error(w, "Method not allowed", 405)
					return
				}
				respond(w, map[string]any{"url": publicURL, "manifest": s.ManifestArtifact})
			case "/operator/status", "/operator/invite", "/operator/reissue", "/operator/revoke":
				delegate(w, r, "/admin/"+strings.TrimPrefix(path, "/operator/"))
			case "/operator/login":
				if r.Method != "POST" {
					http.Error(w, "Method not allowed", 405)
					return
				}
				var input struct {
					Code string `json:"code"`
				}
				if !adminJSON(w, r, &input) {
					return
				}
				code := strings.ToUpper(strings.TrimSpace(input.Code))
				sessions.Lock()
				defer sessions.Unlock()
				sessions.prune()
				pair, ok := sessions.pairs[code]
				if !ok {
					http.Error(w, "Login code expired or unknown. Start again in your browser.", 400)
					return
				}
				pair.actor = actor
				sessions.pairs[code] = pair
				respond(w, map[string]bool{"approved": true})
			case "/operator/run":
				if r.Method != "POST" {
					http.Error(w, "Method not allowed", 405)
					return
				}
				var input OperatorJobRequest
				if !adminJSON(w, r, &input) {
					return
				}
				job, err := jobs.start(input)
				if err != nil {
					http.Error(w, "Operation refused. Check phase, checkpoint and operator job before retrying.", 409)
					return
				}
				respond(w, job)
			case "/operator/job":
				if r.Method != "POST" {
					http.Error(w, "Method not allowed", 405)
					return
				}
				var input struct {
					ID string `json:"id"`
				}
				if !adminJSON(w, r, &input) {
					return
				}
				if input.ID == "" || input.ID == "latest" {
					if err := ReadJSON(filepath.Join(jobs.directory(), "latest.json"), &input); err != nil {
						http.Error(w, "No coordinator operation yet", 404)
						return
					}
				}
				job, err := jobs.read(input.ID)
				if err != nil {
					http.Error(w, "Operation unavailable", 404)
					return
				}
				respond(w, job)
			default:
				http.NotFound(w, r)
			}
			return
		}
		if r.Method == "GET" && (path == "/" || path == "/admin.js" || path == "/style.css") {
			delegate(w, r, path)
			return
		}
		if r.Header.Get("X-SPR-Admin") != "1" || r.Header.Get("Sec-Fetch-Site") == "cross-site" || (r.Header.Get("Origin") != "" && r.Header.Get("Origin") != gatewayOrigin) || (r.Method != "GET" && r.Header.Get("Origin") != gatewayOrigin) {
			http.Error(w, "Forbidden", 403)
			return
		}
		if strings.HasPrefix(path, "/session/") {
			sessions.handle(w, r)
			return
		}
		sessions.Lock()
		sessions.prune()
		_, authenticated := sessions.sessions[cookieHash(r, browserSessionCookie)]
		sessions.Unlock()
		if !authenticated {
			http.Error(w, "Sign in with AWS to continue.", 401)
			return
		}
		switch path {
		case "/admin/status", "/admin/invite", "/admin/reissue", "/admin/revoke":
			delegate(w, r, path)
		default:
			http.NotFound(w, r)
		}
	}), nil
}

func (s *operatorSessions) handle(w http.ResponseWriter, r *http.Request) {
	s.Lock()
	defer s.Unlock()
	s.prune()
	switch r.Method + " " + r.URL.Path {
	case "GET /session/status":
		expires, ok := s.sessions[cookieHash(r, browserSessionCookie)]
		respond(w, map[string]any{"authenticated": ok, "expires": expires})
	case "POST /session/start":
		ip := net.ParseIP(r.Header.Get("X-SPR-Gateway-IP"))
		if ip == nil {
			http.Error(w, "Invalid gateway request", 403)
			return
		}
		if _, recent := s.starts[ip.String()]; recent || len(s.pairs) >= 128 || len(s.starts) >= 1024 {
			w.Header().Set("Retry-After", "10")
			http.Error(w, "Try again shortly.", 429)
			return
		}
		token, err := randomBrowserToken()
		if err != nil {
			http.Error(w, "Login unavailable", 503)
			return
		}
		code, err := pairingCode()
		if err != nil {
			http.Error(w, "Login unavailable", 503)
			return
		}
		if _, collision := s.pairs[code]; collision {
			http.Error(w, "Try again", 503)
			return
		}
		old := cookieHash(r, browserPairingCookie)
		for code, pair := range s.pairs {
			if pair.hash == old {
				delete(s.pairs, code)
			}
		}
		s.pairs[code] = operatorPairing{hash: hashBytes([]byte(token)), expires: s.now().Add(5 * time.Minute)}
		s.starts[ip.String()] = s.now().Add(10 * time.Second)
		operatorCookie(w, browserPairingCookie, token, 5*time.Minute)
		respond(w, map[string]any{"code": code, "expires_in": 300})
	case "GET /session/poll":
		hash := cookieHash(r, browserPairingCookie)
		for code, pair := range s.pairs {
			if hash == "" || pair.hash != hash {
				continue
			}
			if pair.actor == "" {
				respond(w, map[string]bool{"authenticated": false})
				return
			}
			if len(s.sessions) >= 128 {
				http.Error(w, "Too many active sessions", 429)
				return
			}
			token, err := randomBrowserToken()
			if err != nil {
				http.Error(w, "Login unavailable", 503)
				return
			}
			expires := s.now().Add(browserSessionTTL)
			s.sessions[hashBytes([]byte(token))] = expires
			delete(s.pairs, code)
			operatorCookie(w, browserPairingCookie, "", 0)
			operatorCookie(w, browserSessionCookie, token, browserSessionTTL)
			respond(w, map[string]any{"authenticated": true, "expires": expires})
			return
		}
		http.Error(w, "Login expired. Start again.", 401)
	case "POST /session/logout":
		delete(s.sessions, cookieHash(r, browserSessionCookie))
		operatorCookie(w, browserSessionCookie, "", 0)
		operatorCookie(w, browserPairingCookie, "", 0)
		respond(w, map[string]bool{"signed_out": true})
	default:
		http.NotFound(w, r)
	}
}
