package ceremony

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/helius-labs/spr-ceremony/internal/signing"
)

// GenerateSigningKey creates a dedicated key pair without overwriting files.
// The private key must be backed up separately from public ceremony storage.
func GenerateSigningKey(privatePath, publicPath string) error {
	key, err := signing.New()
	if err != nil {
		return err
	}
	private, err := key.Private()
	if err != nil {
		return err
	}
	for _, path := range []string{privatePath, publicPath} {
		if path == "" {
			return fmt.Errorf("both key paths are required")
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return fmt.Errorf("key output must be new")
		}
	}
	for _, item := range []struct {
		path string
		data []byte
	}{{privatePath, private}, {publicPath, key.Public()}} {
		if err = os.MkdirAll(filepath.Dir(item.path), 0700); err != nil {
			return err
		}
		f, e := os.OpenFile(item.path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		_, e = f.Write(item.data)
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e != nil {
			return e
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (s *Store) PublicSigningKey() ([]byte, error) {
	ref := s.Manifest.CoordinatorKey
	if ref == nil {
		if s.Manifest.Phase1.UsesTestParameters() {
			return nil, nil
		}
		return nil, fmt.Errorf("production ceremony requires a manifest-bound coordinator signing key")
	}
	if ref.Validate() != nil || ref.Size > signing.MaxKeyBytes {
		return nil, fmt.Errorf("invalid coordinator public key reference")
	}
	if err := Check(s.Object(*ref), *ref); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(s.Object(*ref))
	if err != nil {
		return nil, err
	}
	if _, err = signing.PublicFingerprint(b); err != nil {
		return nil, err
	}
	return b, nil
}

func (s *Store) LoadSigningKey(path string) error {
	st, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("coordinator signing key unavailable")
	}
	if !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 || st.Size() > signing.MaxKeyBytes {
		return fmt.Errorf("coordinator signing key must be a private regular file (0600)")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("coordinator signing key unavailable")
	}
	key, err := signing.LoadPrivate(b)
	if err != nil {
		return err
	}
	public, err := s.PublicSigningKey()
	if err != nil {
		return err
	}
	if !bytes.Equal(key.Public(), public) {
		return fmt.Errorf("signing key does not match ceremony manifest")
	}
	s.Signer = key
	return nil
}

func (s *Store) RequireSigningKey() error {
	if s.Manifest.CoordinatorKey == nil && s.Manifest.Phase1.UsesTestParameters() {
		return nil
	}
	public, err := s.PublicSigningKey()
	if err != nil {
		return err
	}
	if s.Signer == nil || !bytes.Equal(s.Signer.Public(), public) {
		return fmt.Errorf("coordinator signing key is required")
	}
	return nil
}

func (s *Store) signMessage(kind string, value any) (string, error) {
	if err := s.RequireSigningKey(); err != nil {
		return "", err
	}
	if s.Manifest.CoordinatorKey == nil {
		return "", nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	payload, err := signing.Payload(kind, s.ManifestArtifact.SHA256, b)
	if err != nil {
		return "", err
	}
	return s.Signer.Sign(payload)
}
func (s *Store) signInvitation(i *Invitation) error {
	sig, err := s.signMessage("invitation", i)
	i.Signature = sig
	return err
}
func (s *Store) signReceipt(r *Receipt) error {
	sig, err := s.signMessage("receipt", r)
	r.Signature = sig
	return err
}
func (s *Store) verifyMessage(kind string, value any) error {
	public, err := s.PublicSigningKey()
	if err != nil {
		return err
	}
	if len(public) == 0 {
		return nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return signing.VerifyMessage(public, kind, s.ManifestArtifact.SHA256, b)
}

// BindSigningKey makes a NEW prepared campaign from untouched parameters. It
// never rewrites an existing manifest or changes an opened campaign's identity.
// Immutable artifacts are hard-linked on the same filesystem, copied otherwise.
func (s *Store) BindSigningKey(out string, public []byte) error {
	if _, err := signing.PublicFingerprint(public); err != nil {
		return err
	}
	unlock, err := s.lock()
	if err != nil {
		return err
	}
	defer unlock()
	cp, checkpoint, err := s.checkpoint()
	if err != nil {
		return err
	}
	if cp.Sequence != 0 || cp.Previous != nil || cp.Lifecycle.Phase != "prepared" && !s.Manifest.Phase1.UsesTestParameters() || len(cp.Exports) != 0 {
		return fmt.Errorf("signing identity can only be bound to untouched parameters in a new directory")
	}
	if s.Remote != nil {
		return fmt.Errorf("bind identity before configuring remote publication")
	}
	if err = s.AuditCheckpoints(); err != nil {
		return err
	}
	if _, err = os.Stat(out); !os.IsNotExist(err) {
		return fmt.Errorf("destination must be new")
	}
	if err = os.MkdirAll(filepath.Dir(out), 0700); err != nil {
		return err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(out), ".bind-key-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	clone := &Store{Dir: tmp, Manifest: s.Manifest}
	refs, err := s.references(cp, checkpoint)
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if err = Check(s.Object(ref), ref); err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Join(tmp, "objects"), 0700); err != nil {
			return err
		}
		if err = os.Link(s.Object(ref), clone.Object(ref)); err != nil {
			if _, err = clone.put(s.Object(ref)); err != nil {
				return err
			}
		}
	}
	keyPath := filepath.Join(tmp, "public.asc")
	if err = os.WriteFile(keyPath, public, 0600); err != nil {
		return err
	}
	keyRef, err := clone.put(keyPath)
	if err != nil {
		return err
	}
	clone.Manifest.CoordinatorKey = &keyRef
	if err = os.Remove(keyPath); err != nil {
		return err
	}
	for _, c := range clone.Manifest.Circuits {
		st, e := s.State(c.Spec.ID)
		if e != nil {
			return e
		}
		if len(st.History) != 0 {
			return fmt.Errorf("parameters already have contributions")
		}
		if err = WriteJSON(clone.statePath(c.Spec.ID), st); err != nil {
			return err
		}
	}
	if err = WriteJSON(filepath.Join(tmp, "manifest.json"), clone.Manifest); err != nil {
		return err
	}
	clone.ManifestArtifact, err = Digest(filepath.Join(tmp, "manifest.json"))
	if err != nil {
		return err
	}
	if err = clone.initializeCheckpoint(); err != nil {
		return err
	}
	return os.Rename(tmp, out)
}

func writePublicFile(path string, b []byte) error {
	return Atomic(path, func(w io.Writer) error { _, err := w.Write(b); return err })
}
