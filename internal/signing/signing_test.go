package signing

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	openpgp "github.com/ProtonMail/go-crypto/openpgp/v2"
)

func TestSignaturesAndStablePublicKey(t *testing.T) {
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	private, err := s.Private()
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := LoadPrivate(private)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reloaded.Public(), s.Public()) || reloaded.Fingerprint() != s.Fingerprint() {
		t.Fatal("key identity changed after serialization")
	}
	payload := []byte("SPR signed coordinator message\n")
	signature, err := reloaded.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err = Verify(s.Public(), payload, signature); err != nil {
		t.Fatal(err)
	}
	if Verify(s.Public(), []byte("changed message"), signature) == nil {
		t.Fatal("accepted changed payload")
	}
	other, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if Verify(other.Public(), payload, signature) == nil {
		t.Fatal("accepted another coordinator")
	}
	if Verify(private, payload, signature) == nil {
		t.Fatal("accepted private key material as a public key")
	}
}

func TestSignatureTimeWindowDiagnostic(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	keyConfig := config()
	keyConfig.Time = func() time.Time { return now.Add(-2 * time.Hour) }
	entity, err := openpgp.NewEntity("Time window test", "", "", keyConfig)
	if err != nil {
		t.Fatal(err)
	}
	entity.Subkeys = nil
	s, err := signer(entity)
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("signed receipt for time validation")
	for _, tc := range []struct {
		name     string
		created  time.Time
		lifetime uint32
		valid    bool
	}{
		{"valid", now.Add(-time.Minute), 0, true},
		{"future", now.Add(time.Hour), 0, false},
		{"expired", now.Add(-time.Hour), 60, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config()
			cfg.Time = func() time.Time { return tc.created }
			cfg.SigLifetimeSecs = tc.lifetime
			var signature bytes.Buffer
			if err := openpgp.ArmoredDetachSign(&signature, []*openpgp.Entity{entity}, bytes.NewReader(payload), &openpgp.SignParams{Config: cfg}); err != nil {
				t.Fatal(err)
			}
			err := Verify(s.Public(), payload, signature.String())
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "outside its valid time window") || !strings.Contains(err.Error(), "automatic date and time") {
				t.Fatalf("expected time window rejection with recovery instructions, got %v", err)
			}
			// Payload tampering must still fail cryptographic verification, even
			// when the original signature is also outside its time window.
			err = Verify(s.Public(), []byte("changed receipt"), signature.String())
			if err == nil || err.Error() != "invalid coordinator signature" {
				t.Fatalf("expected tampering rejection without clock advice, got %v", err)
			}
		})
	}
	if err := Verify(s.Public(), payload, "not a signature"); err == nil || err.Error() != "invalid coordinator signature" {
		t.Fatalf("expected malformed signature rejection, got %v", err)
	}
}

func TestGPGInteroperability(t *testing.T) {
	gpg, err := exec.LookPath("gpg")
	if err != nil {
		t.Skip("GnuPG is required for independent interoperability verification")
	}
	s, err := New()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("SPR independent GnuPG interoperability check\n")
	sig, err := s.Sign(payload)
	if err != nil {
		t.Fatal(err)
	}
	private, err := s.Private()
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{"public.asc": s.Public(), "key.private.asc": private, "message": payload, "message.asc": []byte(sig)} {
		if err = os.WriteFile(filepath.Join(dir, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command(gpg, append([]string{"--homedir", dir, "--batch", "--no-tty"}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("GnuPG failed: %v: %s", err, output)
		}
	}
	run("--import", filepath.Join(dir, "public.asc"))
	run("--verify", filepath.Join(dir, "message.asc"), filepath.Join(dir, "message"))
	run("--import", filepath.Join(dir, "key.private.asc"))
	run("--pinentry-mode", "loopback", "--passphrase", "", "--armor", "--detach-sign", "--digest-algo", "SHA256", "--local-user", s.Fingerprint(), "--output", filepath.Join(dir, "gpg.asc"), filepath.Join(dir, "message"))
	fromGPG, err := os.ReadFile(filepath.Join(dir, "gpg.asc"))
	if err != nil {
		t.Fatal(err)
	}
	if err = Verify(s.Public(), payload, string(fromGPG)); err != nil {
		t.Fatal(err)
	}
}
