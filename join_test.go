package ceremony

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestPrivateLinkJoinAndResume(t *testing.T) {
	s := demoStore(t)
	handler := s.Handler()
	var uris []string
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		uris = append(uris, r.RequestURI)
		mu.Unlock()
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	invitation, err := s.InviteQueued(server.URL, "joiner", Core, time.Hour)
	must(t, err)
	link, err := InvitationLink(invitation)
	must(t, err)
	client := testClient(t)
	var progress bytes.Buffer
	client.Progress = &progress
	workspace := filepath.Join(client.Work, invitation.ManifestSHA256, invitation.ID)
	must(t, os.MkdirAll(workspace, 0700))
	lock, err := os.OpenFile(filepath.Join(workspace, ".participant.lock"), os.O_CREATE|os.O_RDWR, 0600)
	must(t, err)
	must(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
	if err = client.Join(context.Background(), link); err == nil {
		t.Fatal("concurrent workspace accepted")
	}
	must(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_UN))
	must(t, lock.Close())
	must(t, client.Join(context.Background(), link))
	must(t, client.Join(context.Background(), link))
	for _, name := range []string{"demo_plain", "demo_commitment"} {
		state, err := s.State(name)
		must(t, err)
		if len(state.History) != 1 {
			t.Fatal("resume duplicated contribution")
		}
	}
	mu.Lock()
	defer mu.Unlock()
	for _, uri := range uris {
		if strings.Contains(uri, invitation.Token) {
			t.Fatal("private token in HTTP URI")
		}
	}
	if strings.Contains(progress.String(), invitation.Token) {
		t.Fatal("private token in progress output")
	}
	must(t, filepath.WalkDir(client.Work, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte(invitation.Token)) {
			return fmt.Errorf("private token persisted in public work")
		}
		return nil
	}))
}

func TestInvalidPrivateLinksNeverEchoSecrets(t *testing.T) {
	token := strings.Repeat("b", 64)
	pin := strings.Repeat("a", 64)
	valid := "https://ceremony.example/#token=" + token + "&manifest=" + pin
	origin, gotToken, gotPin, err := ParseInvitationLink(valid)
	must(t, err)
	if origin != "https://ceremony.example" || gotToken != token || gotPin != pin {
		t.Fatal("link parsing changed identity")
	}
	for _, bad := range []string{
		"https://ceremony.example/", // The browser removes the private fragment.
		"'" + valid + "'", "<" + valid + ">", "[Invitation](" + valid + ")",
		"https://ceremony.example/#token=" + token,
		"https://ceremony.example/#manifest=" + pin,
		"http://ceremony.example/#token=" + token + "&manifest=" + pin,
		"https://user:" + token + "@ceremony.example/#token=" + token + "&manifest=" + pin,
		strings.Replace(valid, "/#", "/path#", 1), valid + "&extra=1", valid + "&token=" + token,
		strings.Replace(valid, "/#", "/?token="+token+"#", 1), valid + "%broken", strings.Repeat("x", 8193) + token,
	} {
		_, _, _, err := ParseInvitationLink(bad)
		if err == nil || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), bad) {
			t.Fatal("unsafe link accepted or echoed")
		}
		if !strings.Contains(err.Error(), "Copy invitation") {
			t.Fatal("missing private-link recovery instructions")
		}
	}
}

func TestJoinRejectsResponseBeforeContribution(t *testing.T) {
	token, pin := strings.Repeat("b", 64), strings.Repeat("a", 64)
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/invitation" {
			called = true
		}
		w.WriteHeader(401)
		_, _ = w.Write([]byte(token))
	}))
	defer server.Close()
	err := testClient(t).Join(context.Background(), server.URL+"/#token="+token+"&manifest="+pin)
	if err == nil || strings.Contains(err.Error(), token) || called {
		t.Fatal("rejected invitation reached contribution or exposed response")
	}
	transport := &url.Error{Op: "Get", URL: "https://storage.example/object?X-Amz-Signature=" + token, Err: fmt.Errorf("connection interrupted")}
	if strings.Contains(safeRequestError(transport).Error(), token) {
		t.Fatal("signed query appeared in error")
	}
}

func TestQueueErrorsDoNotEchoPrivateResponse(t *testing.T) {
	token := strings.Repeat("c", 64)
	if err := decodeRemoteJSON(strings.NewReader(`{"`+token+`":true}`), &Claim{}); err == nil || strings.Contains(err.Error(), token) {
		t.Fatal("private field name echoed")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(409); _, _ = w.Write([]byte(token)) }))
	defer server.Close()
	_, err := testClient(t).queueRequest(context.Background(), Invitation{URL: server.URL, Token: token}, "/v1/claim")
	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatal("private response echoed")
	}
}
