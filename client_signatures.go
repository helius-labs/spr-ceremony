package ceremony

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/helius-labs/spr-ceremony/internal/signing"
)

func verifySignedJSON(public []byte, kind, manifest string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return signing.VerifyMessage(public, kind, manifest, b)
}

func (c *Client) authenticateSigningKey(ctx context.Context, i *Invitation, m Manifest, manifestBytes []byte) error {
	i.signingPublicKey = nil
	if m.CoordinatorKey == nil {
		if !m.Phase1.UsesTestParameters() || i.Signature != "" {
			return fmt.Errorf("manifest has no coordinator signing key")
		}
		return nil
	}
	if m.CoordinatorKey.Validate() != nil || m.CoordinatorKey.Size > signing.MaxKeyBytes {
		return fmt.Errorf("invalid coordinator signing key reference")
	}
	keyPath := filepath.Join(c.Work, i.ManifestSHA256, "coordinator.asc")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(i.URL, "/")+"/v1/objects/"+m.CoordinatorKey.SHA256, nil)
	if err != nil {
		return err
	}
	response, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	public, err := io.ReadAll(io.LimitReader(response.Body, signing.MaxKeyBytes+1))
	if err != nil {
		return err
	}
	if response.StatusCode != 200 || int64(len(public)) != m.CoordinatorKey.Size || hashBytes(public) != m.CoordinatorKey.SHA256 {
		return fmt.Errorf("coordinator key checksum mismatch")
	}
	if err = verifySignedJSON(public, "invitation", i.ManifestSHA256, i); err != nil {
		return err
	}
	if err = writePublicFile(keyPath, public); err != nil {
		return err
	}
	if err = writePublicFile(filepath.Join(c.Work, i.ManifestSHA256, "manifest.json"), manifestBytes); err != nil {
		return err
	}
	i.signingPublicKey = public
	return nil
}

func (c *Client) verifyReceipt(i Invitation, r Receipt) error {
	if len(i.signingPublicKey) == 0 {
		if !i.InsecureTestSetup || i.Signature != "" || r.Signature != "" {
			return fmt.Errorf("receipt signing key is missing")
		}
		return nil
	}
	return verifySignedJSON(i.signingPublicKey, "receipt", i.ManifestSHA256, r)
}

// retrySignatureTimeWindow never changes the verifier's clock or trusts the
// coordinator's clock. Wait only for the typed time-window failure, at most ten
// seconds, without repeating an upload or contribution.
func retrySignatureTimeWindow(ctx context.Context, verify func() error, waiting func()) error {
	deadline := time.Now().Add(10 * time.Second)
	announced := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := verify()
		if !errors.Is(err, signing.ErrSignatureTimeWindow) || !time.Now().Before(deadline) {
			return err
		}
		if !announced {
			waiting()
			announced = true
		}
		wait := time.NewTimer(min(time.Second, time.Until(deadline)))
		select {
		case <-ctx.Done():
			wait.Stop()
			return ctx.Err()
		case <-wait.C:
		}
	}
}

func offlineReceiptVerifier(work string, i Invitation) (func(Receipt) error, error) {
	c := &Client{}
	if i.Signature != "" || !i.InsecureTestSetup {
		dir := filepath.Join(work, i.ManifestSHA256)
		manifestPath := filepath.Join(dir, "manifest.json")
		ref, err := Digest(manifestPath)
		if err != nil || ref.SHA256 != i.ManifestSHA256 {
			return nil, fmt.Errorf("authenticated local manifest is missing")
		}
		var m Manifest
		if err = ReadJSON(manifestPath, &m); err != nil {
			return nil, err
		}
		if m.CoordinatorKey == nil {
			return nil, fmt.Errorf("coordinator key is not bound to manifest")
		}
		keyPath := filepath.Join(dir, "coordinator.asc")
		if err = Check(keyPath, *m.CoordinatorKey); err != nil {
			return nil, err
		}
		i.signingPublicKey, err = os.ReadFile(keyPath)
		if err != nil {
			return nil, err
		}
		if err = verifySignedJSON(i.signingPublicKey, "invitation", i.ManifestSHA256, i); err != nil {
			return nil, err
		}
	}
	return func(r Receipt) error { return c.verifyReceipt(i, r) }, nil
}
