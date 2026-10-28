package ceremony

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

func operatorFixture(t *testing.T) (*Store, *Operator, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "ceremony")
	must(t, DemoSigned(dir, false))
	s, err := Open(dir)
	must(t, err)
	public := httptest.NewServer(s.Handler())
	t.Cleanup(public.Close)
	control := httptest.NewUnstartedServer(nil)
	controlURL := "http://" + control.Listener.Addr().String()
	control.Config.Handler, err = s.AdminHandler(controlURL, public.URL)
	must(t, err)
	control.Start()
	t.Cleanup(control.Close)
	config, err := ConnectOperator(context.Background(), public.URL, controlURL)
	must(t, err)
	operator, err := NewOperator(config)
	must(t, err)
	return s, operator, public.URL
}

func TestOperatorInvitesAuditsAndRevokesThroughControlTunnel(t *testing.T) {
	s, operator, _ := operatorFixture(t)
	ctx := context.Background()
	i, err := operator.Invite(ctx, "alice", 24)
	must(t, err)
	claim, err := s.Claim(i.Token, time.Minute)
	must(t, err)
	output := filepath.Join(t.TempDir(), "contribution.ph2")
	must(t, Contribute(s.Object(claim.Job.Input), output, claim.Job.Circuit.Shape))
	_, err = s.Accept(i.Token, claim.Job.Circuit.Spec.ID, output)
	must(t, err)
	status, err := operator.Status(ctx)
	must(t, err)
	if len(status.Invitations) != 1 || status.Invitations[0].Accepted != 1 {
		t.Fatal("partial completion was not preserved")
	}
	if _, err = operator.Invite(ctx, "alice", 24); err == nil {
		t.Fatal("duplicate active pseudonym accepted")
	}
	renewed, err := operator.Reissue(ctx, i.ID, 24)
	must(t, err)
	if renewed.ID != i.ID || renewed.Token == i.Token {
		t.Fatal("reissue lost identity or reused private token")
	}
	if _, err = s.Claim(i.Token, time.Minute); err == nil {
		t.Fatal("old invitation remained active")
	}
	participant := NewClient(t.TempDir())
	participant.Progress = io.Discard
	must(t, participant.Run(ctx, renewed, Core))
	status, err = operator.Status(ctx)
	must(t, err)
	if status.Invitations[0].Accepted != 2 {
		t.Fatal("native resume did not complete remaining work")
	}
	work := t.TempDir()
	point, snapshot, err := operator.Audit(ctx, work, 64, io.Discard)
	must(t, err)
	if point.Manifest != s.ManifestArtifact || point.Checkpoint != status.Checkpoint {
		t.Fatal("audit changed snapshot")
	}
	for _, private := range []string{"invitations", "private", "leases", "s3.json"} {
		if _, err = os.Stat(filepath.Join(snapshot, private)); !os.IsNotExist(err) {
			t.Fatal("private coordinator data entered the public audit")
		}
	}
	// Cached public bytes must support a second independent verification.
	_, cached, err := operator.Audit(ctx, work, 64, io.Discard)
	must(t, err)
	if cached != snapshot {
		t.Fatal("unchanged checkpoint was not reused")
	}
	must(t, operator.Revoke(ctx, i.ID))
	must(t, operator.Revoke(ctx, i.ID))
	if _, err = s.Claim(renewed.Token, time.Minute); err == nil {
		t.Fatal("revoked link remained active")
	}
	status, err = operator.Status(ctx)
	must(t, err)
	if status.Invitations[0].HasLink || status.Invitations[0].Accepted != 2 {
		t.Fatal("revocation lost contributions or retained access")
	}
	must(t, filepath.WalkDir(work, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if bytes.Contains(data, []byte(i.Token)) || bytes.Contains(data, []byte(renewed.Token)) {
				t.Fatal("bearer token persisted in public audit data")
			}
		}
		return nil
	}))
}

func TestOperatorPinsCampaignBeforeMutations(t *testing.T) {
	s, operator, _ := operatorFixture(t)
	wrong := operator.Config
	wrong.Manifest.SHA256 = strings.Repeat("0", 64)
	other, err := NewOperator(wrong)
	must(t, err)
	if _, err = other.Invite(context.Background(), "blocked", 24); err == nil {
		t.Fatal("changed campaign accepted")
	}
	// The server also rejects a campaign change between status and mutation.
	request, err := http.NewRequest("POST", operator.Config.ControlURL+"/admin/invite", strings.NewReader(`{"name":"blocked","hours":24}`))
	must(t, err)
	request.Header.Set("Origin", operator.Config.ControlURL)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-SPR-Admin", "1")
	request.Header.Set("X-SPR-Manifest", wrong.Manifest.SHA256)
	response, err := http.DefaultClient.Do(request)
	must(t, err)
	response.Body.Close()
	if response.StatusCode != 409 {
		t.Fatal("mutation was not bound to its manifest")
	}
	stored, err := s.storedInvitations()
	must(t, err)
	if len(stored) != 0 {
		t.Fatal("wrong campaign request created an invitation")
	}
	for _, raw := range []string{"https://example.com", "http://localhost:8081", "http://192.168.1.1:8081", "http://127.0.0.1:8081/path", "http://127.0.0.1:8081?token=secret"} {
		wrong = operator.Config
		wrong.ControlURL = raw
		if _, err = NewOperator(wrong); err == nil {
			t.Fatal("unsafe control origin accepted")
		}
	}
}

type operatorRoundTrip func(*http.Request) (*http.Response, error)

func (f operatorRoundTrip) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestOperatorSignsExactGatewayRequestAndFailsClosed(t *testing.T) {
	_, operator, _ := operatorFixture(t)
	operator.Config.ControlURL = ""
	operator.Config.GatewayURL = "https://example.execute-api.eu-north-1.amazonaws.com"
	credentials := aws.Credentials{AccessKeyID: "fixture-access", SecretAccessKey: "fixture-secret", SessionToken: "fixture-session"}
	operator.credentials = aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) { return credentials, nil })
	calls := 0
	operator.client.HTTP.Transport = operatorRoundTrip(func(request *http.Request) (*http.Response, error) {
		calls++
		if request.Method != "POST" || request.URL.String() != operator.Config.GatewayURL+"/operator/login" || request.Header.Get("X-SPR-Manifest") != operator.Config.Manifest.SHA256 {
			t.Fatal("operator request escaped its gateway or manifest pin")
		}
		body, err := io.ReadAll(request.Body)
		must(t, err)
		if string(body) != `{"code":"ABCD-EFGH-JKLM"}` {
			t.Fatal("unexpected signed body")
		}
		at, err := time.Parse("20060102T150405Z", request.Header.Get("X-Amz-Date"))
		must(t, err)
		expected := request.Clone(context.Background())
		expected.Header.Del("Authorization")
		digest := sha256.Sum256(body)
		must(t, v4.NewSigner().SignHTTP(context.Background(), credentials, expected, hex.EncodeToString(digest[:]), "execute-api", "eu-north-1", at))
		if expected.Header.Get("Authorization") != request.Header.Get("Authorization") || request.Header.Get("X-Amz-Security-Token") != credentials.SessionToken {
			t.Fatal("AWS signing did not bind the complete request")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"approved":true}`)), Header: make(http.Header)}, nil
	})
	must(t, operator.ApproveBrowser(context.Background(), "ABCD-EFGH-JKLM"))
	operator.credentials = aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{}, fmt.Errorf("private credential detail")
	})
	err := operator.ApproveBrowser(context.Background(), "ABCD-EFGH-JKLM")
	if err == nil || strings.Contains(err.Error(), "private credential detail") || calls != 1 {
		t.Fatal("unavailable credentials were sent or exposed")
	}
	operator.credentials = aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) { return credentials, nil })
	for _, origin := range []string{"http://example.execute-api.eu-north-1.amazonaws.com", "https://example.com", "https://example.execute-api.eu-north-1.amazonaws.com.evil.test", operator.Config.GatewayURL + "/path"} {
		operator.Config.GatewayURL = origin
		if operator.ApproveBrowser(context.Background(), "ABCD-EFGH-JKLM") == nil || calls != 1 {
			t.Fatal("credentials sent to an unapproved origin")
		}
	}
}

func TestOperatorRejectsForgedInvitationsAndCorruptPublicObjects(t *testing.T) {
	_, operator, _ := operatorFixture(t)
	transport := http.DefaultTransport
	operator.client.HTTP.Transport = operatorRoundTrip(func(request *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(request)
		if err == nil && request.URL.Path == "/admin/invite" {
			var value map[string]any
			err = json.NewDecoder(response.Body).Decode(&value)
			response.Body.Close()
			if err != nil {
				return nil, err
			}
			value["invitation"].(map[string]any)["signature"] = "forged"
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, err
			}
			response.Body = io.NopCloser(bytes.NewReader(encoded))
		}
		return response, err
	})
	if _, err := operator.Invite(context.Background(), "forged", 24); err == nil {
		t.Fatal("forged coordinator signature accepted")
	}
	operator.client.HTTP.Transport = operatorRoundTrip(func(request *http.Request) (*http.Response, error) {
		response, err := transport.RoundTrip(request)
		if err == nil && strings.HasPrefix(request.URL.Path, "/v1/objects/") {
			data, readErr := io.ReadAll(response.Body)
			response.Body.Close()
			if readErr != nil {
				return nil, readErr
			}
			if len(data) > 0 {
				data[0] ^= 1
			}
			response.Body = io.NopCloser(bytes.NewReader(data))
		}
		return response, err
	})
	if _, _, err := operator.Audit(context.Background(), t.TempDir(), 64, io.Discard); err == nil {
		t.Fatal("corrupt pinned manifest accepted")
	}
}

func TestOperatorAuditRestoresFinalKeysWithoutCoordinatorCredentials(t *testing.T) {
	s, operator, _ := operatorFixture(t)
	// Start this marked fixture prepared to exercise the complete beacon path.
	cp, _, err := s.checkpoint()
	must(t, err)
	cp.Lifecycle = Lifecycle{Phase: "prepared"}
	ref, err := s.putJSON(cp)
	must(t, err)
	must(t, WriteJSON(filepath.Join(s.Dir, "checkpoint.json"), ref))
	now := time.Now().UTC().Truncate(time.Second)
	plan, proof := testBeacon(t, now.Add(2*time.Hour))
	s.now = func() time.Time { return now }
	_, err = s.OpenCeremony(plan)
	must(t, err)
	invitation, err := operator.Invite(context.Background(), "final-review", 24)
	must(t, err)
	client := NewClient(t.TempDir())
	client.Progress = io.Discard
	must(t, client.Run(context.Background(), invitation, Core))
	_, err = s.CloseCeremony()
	must(t, err)
	s.now = func() time.Time { return plan.RoundTime.Add(time.Second) }
	_, err = s.Finalize(filepath.Join(t.TempDir(), "coordinator-export"), proof, 64)
	must(t, err)
	out := filepath.Join(t.TempDir(), "independent-export")
	work := t.TempDir()
	point, directory, err := operator.Export(context.Background(), work, out, 64, io.Discard)
	must(t, err)
	public, err := Open(directory)
	must(t, err)
	life, checkpoint, err := public.Lifecycle()
	must(t, err)
	if checkpoint != point.Checkpoint || life.Phase != "finalized" || public.ManifestArtifact != point.Manifest {
		t.Fatal("final public snapshot did not preserve its pins")
	}
	// Repeating an export verifies existing output without replacing it.
	_, _, err = operator.Export(context.Background(), work, out, 64, io.Discard)
	must(t, err)
	// A locally changed key must not be accepted from a previously audited store.
	key := filepath.Join(out, public.Manifest.Circuits[0].Spec.ID+".pk")
	b, err := os.ReadFile(key)
	must(t, err)
	b[0] ^= 1
	must(t, os.WriteFile(key, b, 0600))
	if _, _, err = operator.Export(context.Background(), work, out, 64, io.Discard); err == nil {
		t.Fatal("changed final proving key accepted")
	}
}
