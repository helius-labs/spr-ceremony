// Package signing implements the ceremony coordinator's OpenPGP identity.
// It is shared by the native client and the browser verification worker.
package signing

import (
	"bytes"
	"crypto"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	openpgp "github.com/ProtonMail/go-crypto/openpgp/v2"
)

const MaxKeyBytes = 16 << 10
const MaxSignatureBytes = 8 << 10
const MaxPayloadBytes = 256 << 10

type Signer struct {
	mu          sync.Mutex
	entity      *openpgp.Entity
	public      []byte
	fingerprint string
}

func config() *packet.Config {
	return &packet.Config{Algorithm: packet.PubKeyAlgoEdDSA, Curve: packet.Curve25519, DefaultHash: crypto.SHA256}
}

func New() (*Signer, error) {
	entity, err := openpgp.NewEntity("Solana Privacy Rings", "Coordinator signing key", "", config())
	if err != nil {
		return nil, fmt.Errorf("cannot generate coordinator signing key")
	}
	// The coordinator signs only; do not retain an unused encryption subkey.
	entity.Subkeys = nil
	return signer(entity)
}

func read(data []byte, private bool) (*openpgp.Entity, error) {
	kind := "PGP PUBLIC KEY BLOCK"
	if private {
		kind = "PGP PRIVATE KEY BLOCK"
	}
	if len(data) == 0 || len(data) > MaxKeyBytes {
		return nil, fmt.Errorf("invalid coordinator key size")
	}
	block, err := armor.Decode(bytes.NewReader(data))
	if err != nil || block.Type != kind {
		return nil, fmt.Errorf("invalid coordinator key encoding")
	}
	keys, err := openpgp.ReadArmoredKeyRing(bytes.NewReader(data))
	if err != nil || len(keys) != 1 {
		return nil, fmt.Errorf("invalid coordinator key")
	}
	key := keys[0]
	if key.PrimaryKey == nil || key.PrimaryKey.Version != 4 || key.PrimaryKey.PubKeyAlgo != packet.PubKeyAlgoEdDSA || len(key.Identities) != 1 || len(key.Subkeys) != 0 || len(key.Revocations) != 0 {
		return nil, fmt.Errorf("use a dedicated Ed25519 coordinator key")
	}
	if private && (key.PrivateKey == nil || key.PrivateKey.Encrypted) || !private && key.PrivateKey != nil {
		return nil, fmt.Errorf("incorrect coordinator key type")
	}
	return key, nil
}

func LoadPrivate(data []byte) (*Signer, error) {
	entity, err := read(data, true)
	if err != nil {
		return nil, err
	}
	return signer(entity)
}

func signer(entity *openpgp.Entity) (*Signer, error) {
	var public bytes.Buffer
	w, err := armor.Encode(&public, "PGP PUBLIC KEY BLOCK", nil)
	if err != nil {
		return nil, err
	}
	if err = entity.Serialize(w); err != nil {
		return nil, err
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	return &Signer{entity: entity, public: public.Bytes(), fingerprint: hex.EncodeToString(entity.PrimaryKey.Fingerprint)}, nil
}

func (s *Signer) Public() []byte      { return bytes.Clone(s.public) }
func (s *Signer) Fingerprint() string { return s.fingerprint }
func (s *Signer) String() string      { return "coordinator signing key " + s.fingerprint }

// Private is only used by explicit operator key creation, never by an HTTP API.
func (s *Signer) Private() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var private bytes.Buffer
	w, err := armor.Encode(&private, "PGP PRIVATE KEY BLOCK", nil)
	if err != nil {
		return nil, err
	}
	if err = s.entity.SerializePrivateWithoutSigning(w, config()); err != nil {
		return nil, err
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	return private.Bytes(), nil
}

func PublicFingerprint(data []byte) (string, error) {
	entity, err := read(data, false)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(entity.PrimaryKey.Fingerprint), nil
}

func (s *Signer) Sign(payload []byte) (string, error) {
	if len(payload) == 0 || len(payload) > MaxPayloadBytes {
		return "", fmt.Errorf("invalid signature payload size")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var signature bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&signature, []*openpgp.Entity{s.entity}, bytes.NewReader(payload), &openpgp.SignParams{Config: config()}); err != nil {
		return "", fmt.Errorf("coordinator signing failed")
	}
	return signature.String(), nil
}

func Verify(public, payload []byte, signature string) error {
	if len(payload) == 0 || len(payload) > MaxPayloadBytes || len(signature) == 0 || len(signature) > MaxSignatureBytes {
		return fmt.Errorf("invalid signature payload size")
	}
	entity, err := read(public, false)
	if err != nil {
		return err
	}
	sig, verified, err := openpgp.VerifyArmoredDetachedSignature(openpgp.EntityList{entity}, bytes.NewReader(payload), strings.NewReader(signature), config())
	if err != nil || sig == nil || verified == nil || sig.SigType != packet.SigTypeBinary || sig.Hash != crypto.SHA256 || !bytes.Equal(verified.PrimaryKey.Fingerprint, entity.PrimaryKey.Fingerprint) {
		return fmt.Errorf("invalid coordinator signature")
	}
	return nil
}
