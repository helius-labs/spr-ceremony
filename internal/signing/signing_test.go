package signing

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
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
