package ceremony

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOperatorGatewayIAMAndBrowserSessions(t *testing.T) {
	s, _, public := operatorFixture(t)
	const origin = "https://example.execute-api.eu-north-1.amazonaws.com"
	const role = "arn:aws:iam::558215002830:role/aws-reserved/sso.amazonaws.com/eu-north-1/OperatorRole"
	const actor = "arn:aws:sts::558215002830:assumed-role/OperatorRole/alice"
	handler, err := s.AdminHandlerWithGateway("http://127.0.0.1:8081", public, origin, role)
	must(t, err)
	call := func(method, path, body, principal, requestOrigin, peer string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, origin+path, strings.NewReader(body))
		r.RemoteAddr = peer
		r.Header.Set("Origin", requestOrigin)
		r.Header.Set("X-SPR-Admin", "1")
		r.Header.Set("X-SPR-IAM-Principal", principal)
		r.Header.Set("X-SPR-Manifest", s.ManifestArtifact.SHA256)
		r.Header.Set("X-SPR-Gateway-IP", "198.51.100.4")
		if method == "POST" {
			r.Header.Set("Content-Type", "application/json")
		}
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, principal := range []string{"", "arn:aws:sts::111111111111:assumed-role/OperatorRole/alice", "arn:aws:sts::558215002830:assumed-role/OtherRole/alice", "arn:aws:sts::558215002830:assumed-role/OperatorRole/"} {
		if w := call("GET", "/operator/status", "", principal, origin, "127.0.0.1:1"); w.Code != 403 {
			t.Fatal("unapproved IAM principal accepted")
		}
	}
	if w := call("GET", "/operator/status", "", actor, origin, "192.0.2.1:1"); w.Code != 403 {
		t.Fatal("non-loopback gateway accepted")
	}
	if w := call("GET", "/operator/config", "", actor, origin, "127.0.0.1:1"); w.Code != 200 {
		t.Fatal("IAM connection failed")
	}
	if w := call("GET", "/admin/status", "", actor, origin, "127.0.0.1:1"); w.Code != 401 {
		t.Fatal("identity header bypassed browser authentication")
	}
	if w := call("POST", "/session/start", "{}", "", "https://evil.example", "127.0.0.1:1"); w.Code != 403 {
		t.Fatal("cross-origin pairing allowed")
	}
	start := call("POST", "/session/start", "{}", "", origin, "127.0.0.1:1")
	if start.Code != 200 {
		t.Fatal("pairing did not start")
	}
	var challenge struct {
		Code string `json:"code"`
	}
	must(t, json.Unmarshal(start.Body.Bytes(), &challenge))
	if len(challenge.Code) != 14 {
		t.Fatal("unexpected pairing code")
	}
	pairCookies := start.Result().Cookies()
	if len(pairCookies) != 1 || pairCookies[0].Name != browserPairingCookie || !pairCookies[0].HttpOnly || !pairCookies[0].Secure || pairCookies[0].SameSite != http.SameSiteStrictMode || pairCookies[0].Path != "/" || pairCookies[0].Domain != "" {
		t.Fatal("pairing cookie is not protected")
	}
	if strings.Contains(start.Body.String(), pairCookies[0].Value) {
		t.Fatal("pairing secret exposed to JavaScript")
	}
	if w := call("GET", "/session/poll", "", "", origin, "127.0.0.1:1"); w.Code != 401 {
		t.Fatal("pairing secret was not required")
	}
	poll := call("GET", "/session/poll", "", "", origin, "127.0.0.1:1", pairCookies...)
	if poll.Code != 200 || !strings.Contains(poll.Body.String(), "false") {
		t.Fatal("unapproved browser authenticated")
	}
	body, err := json.Marshal(map[string]string{"code": challenge.Code})
	must(t, err)
	if w := call("POST", "/operator/login", string(body), "", origin, "127.0.0.1:1"); w.Code != 403 {
		t.Fatal("unsigned browser approval accepted")
	}
	if w := call("POST", "/operator/login", string(body), actor, origin, "127.0.0.1:1"); w.Code != 200 {
		t.Fatal("IAM approval failed")
	}
	poll = call("GET", "/session/poll", "", "", origin, "127.0.0.1:1", pairCookies...)
	if poll.Code != 200 || !strings.Contains(poll.Body.String(), "true") {
		t.Fatal("approved browser did not authenticate")
	}
	var cookie *http.Cookie
	for _, value := range poll.Result().Cookies() {
		if value.Name == browserSessionCookie {
			cookie = value
		}
	}
	if cookie == nil || cookie.Value == pairCookies[0].Value || !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != 1800 {
		t.Fatal("session cookie not rotated or protected")
	}
	if strings.Contains(poll.Body.String(), cookie.Value) {
		t.Fatal("session secret exposed to JavaScript")
	}
	if w := call("GET", "/session/poll", "", "", origin, "127.0.0.1:1", pairCookies...); w.Code != 401 {
		t.Fatal("pairing reused after exchange")
	}
	if w := call("POST", "/admin/invite", `{"name":"browser-operator","hours":24}`, "", origin, "127.0.0.1:1", cookie); w.Code != 200 {
		t.Fatal("authenticated invitation failed")
	}
	if w := call("POST", "/admin/invite", `{"name":"csrf","hours":24}`, "", "https://evil.example", "127.0.0.1:1", cookie); w.Code != 403 {
		t.Fatal("cross-origin session mutation accepted")
	}
	if w := call("POST", "/session/logout", "{}", "", origin, "127.0.0.1:1", cookie); w.Code != 200 {
		t.Fatal("logout failed")
	}
	if w := call("GET", "/admin/status", "", "", origin, "127.0.0.1:1", cookie); w.Code != 401 {
		t.Fatal("session survived logout")
	}
}

func TestOperatorSessionsExpireAndBoundPendingWork(t *testing.T) {
	s := newOperatorSessions()
	now := time.Now()
	s.now = func() time.Time { return now }
	s.pairs["old"] = operatorPairing{expires: now.Add(-time.Second)}
	s.sessions["old"] = now.Add(-time.Second)
	s.starts["old"] = now.Add(-time.Second)
	s.prune()
	if len(s.pairs)+len(s.sessions)+len(s.starts) != 0 {
		t.Fatal("expired login state retained")
	}
	for n := 0; n < 128; n++ {
		s.pairs[string(rune(n))] = operatorPairing{expires: now.Add(time.Minute)}
	}
	r := httptest.NewRequest("POST", "/session/start", nil)
	r.Header.Set("X-SPR-Gateway-IP", "198.51.100.1")
	w := httptest.NewRecorder()
	s.handle(w, r)
	if w.Code != 429 || len(s.pairs) != 128 {
		t.Fatal("pending login state was not bounded")
	}
}
