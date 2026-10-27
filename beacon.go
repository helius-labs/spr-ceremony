package ceremony

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/drand/drand/v2/common/chain"
	drandcrypto "github.com/drand/drand/v2/crypto"
)

type BeaconProof struct {
	Round             uint64 `json:"round"`
	Signature         string `json:"signature"`
	Randomness        string `json:"randomness,omitempty"`
	PreviousSignature string `json:"previous_signature,omitempty"`
}
type signedBeacon struct {
	round     uint64
	signature []byte
}

func (b signedBeacon) GetRound() uint64             { return b.round }
func (b signedBeacon) GetSignature() []byte         { return b.signature }
func (b signedBeacon) GetPreviousSignature() []byte { return nil }

func NewBeaconPlan(infoJSON []byte, hash string, round uint64) (BeaconPlan, error) {
	var info chain.Info
	var p BeaconPlan
	if len(infoJSON) > 64<<10 {
		return p, fmt.Errorf("oversized chain info")
	}
	var timing struct {
		Period uint64 `json:"period"`
	}
	if err := json.Unmarshal(infoJSON, &timing); err != nil {
		return p, err
	}
	if timing.Period == 0 || timing.Period > 86400 {
		return p, fmt.Errorf("invalid beacon period")
	}
	if err := json.Unmarshal(infoJSON, &info); err != nil {
		return p, err
	}
	// Restrict to the unchained RFC9380 scheme. Chained networks require an
	// additional chain-verification policy and are intentionally unsupported.
	if info.Scheme != drandcrypto.SigsOnG1ID || info.HashString() != hash {
		return p, fmt.Errorf("unsupported beacon scheme or chain hash mismatch")
	}
	if info.PublicKey == nil || info.PublicKey.Equal(info.PublicKey.Clone().Null()) {
		return p, fmt.Errorf("invalid beacon public key")
	}
	seconds := int64(info.Period / time.Second)
	if seconds <= 0 || seconds > 86400 || info.GenesisTime <= 0 || round == 0 || round-1 > uint64((math.MaxInt64-info.GenesisTime)/seconds) {
		return p, fmt.Errorf("invalid beacon round or time")
	}
	canonical, err := json.Marshal(info)
	if err != nil {
		return p, err
	}
	return BeaconPlan{ChainInfo: canonical, ChainHash: hash, Round: round, RoundTime: time.Unix(info.GenesisTime+int64(round-1)*seconds, 0).UTC()}, nil
}

func VerifyBeacon(plan BeaconPlan, proof BeaconProof) ([]byte, error) {
	checked, err := NewBeaconPlan(plan.ChainInfo, plan.ChainHash, plan.Round)
	if err != nil {
		return nil, err
	}
	if !checked.RoundTime.Equal(plan.RoundTime) || proof.Round != plan.Round || proof.PreviousSignature != "" {
		return nil, fmt.Errorf("beacon does not match the announced plan")
	}
	var info chain.Info
	if err = json.Unmarshal(plan.ChainInfo, &info); err != nil {
		return nil, err
	}
	signature, err := hex.DecodeString(proof.Signature)
	if err != nil || len(signature) != 48 {
		return nil, fmt.Errorf("invalid beacon signature encoding")
	}
	scheme, err := drandcrypto.GetSchemeByID(info.Scheme)
	if err != nil {
		return nil, err
	}
	if err = scheme.VerifyBeacon(signedBeacon{proof.Round, signature}, info.PublicKey); err != nil {
		return nil, fmt.Errorf("beacon signature failed: %w", err)
	}
	randomness := sha256.Sum256(signature)
	if proof.Randomness != "" && proof.Randomness != hex.EncodeToString(randomness[:]) {
		return nil, fmt.Errorf("beacon randomness mismatch")
	}
	return randomness[:], nil
}

// FetchBeacon accepts an exact HTTPS round URL. Authentication comes from the
// pinned chain key and round, never from trusting an endpoint's response alone.
func FetchBeacon(ctx context.Context, raw string, plan BeaconPlan) (BeaconProof, error) {
	var proof BeaconProof
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return proof, fmt.Errorf("beacon URL must use HTTPS")
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return proof, err
	}
	res, err := client.Do(req)
	if err != nil {
		return proof, err
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return proof, fmt.Errorf("beacon HTTP status %d", res.StatusCode)
	}
	if err = decodeJSON(io.LimitReader(res.Body, 64<<10), &proof); err != nil {
		return proof, err
	}
	_, err = VerifyBeacon(plan, proof)
	return proof, err
}

func (s *Store) OpenCeremony(plan BeaconPlan) (Artifact, error) {
	unlock, err := s.lock()
	if err != nil {
		return Artifact{}, err
	}
	defer unlock()
	cp, ref, err := s.checkpoint()
	if err != nil {
		return ref, err
	}
	if cp.Lifecycle.Phase != "prepared" {
		return ref, fmt.Errorf("only a prepared ceremony can be opened")
	}
	checked, err := NewBeaconPlan(plan.ChainInfo, plan.ChainHash, plan.Round)
	if err != nil {
		return ref, err
	}
	if !checked.RoundTime.Equal(plan.RoundTime) || !s.nowUTC().Add(time.Hour).Before(plan.RoundTime) {
		return ref, fmt.Errorf("beacon must be scheduled at least one hour in the future")
	}
	if !s.Manifest.Phase1.InsecureTestSetup {
		core, _ := Select(Core, "")
		if len(s.Manifest.Circuits) != len(core) {
			return ref, fmt.Errorf("production core ceremony requires all 41 circuits")
		}
		for _, c := range s.Manifest.Circuits {
			if c.Spec.Track != Core || !IsCatalogSpec(c.Spec) {
				return ref, fmt.Errorf("production campaign must contain only the agreed core circuits")
			}
		}
	}
	now := s.nowUTC()
	cp.Lifecycle = Lifecycle{Phase: "open", Beacon: &checked, OpenedAt: &now}
	if err = s.commitCheckpoint(cp, ref); err != nil {
		return Artifact{}, err
	}
	_, ref, err = s.checkpoint()
	return ref, err
}

// Finalize binds the exported keys to the closed heads and a verified, planned
// beacon. Repeating it is idempotent; a different beacon/export cannot replace it.
func (s *Store) Finalize(out string, proof BeaconProof, maxDomain uint64) (Artifact, error) {
	unlock, err := s.lock()
	if err != nil {
		return Artifact{}, err
	}
	defer unlock()
	cp, ref, err := s.checkpoint()
	if err != nil {
		return ref, err
	}
	if cp.Lifecycle.Phase != "closed" && cp.Lifecycle.Phase != "finalized" {
		return ref, fmt.Errorf("close the ceremony before finalizing")
	}
	if cp.Lifecycle.Beacon == nil || cp.Lifecycle.ClosedAt == nil {
		return ref, fmt.Errorf("announced beacon and closure are required")
	}
	plan := *cp.Lifecycle.Beacon
	if !cp.Lifecycle.ClosedAt.Before(plan.RoundTime) || s.nowUTC().Before(plan.RoundTime) {
		return ref, fmt.Errorf("invalid closure or beacon timing")
	}
	beacon, err := VerifyBeacon(plan, proof)
	if err != nil {
		return ref, err
	}
	for _, c := range s.Manifest.Circuits {
		st, e := s.State(c.Spec.ID)
		if e != nil {
			return ref, e
		}
		if st.Head != cp.Lifecycle.ClosedHeads[c.Spec.ID] {
			return ref, fmt.Errorf("head changed after closure")
		}
	}
	if cp.Lifecycle.Phase == "finalized" {
		if cp.Lifecycle.FinalExport == nil {
			return ref, fmt.Errorf("missing finalized export")
		}
		if _, e := os.Stat(out); os.IsNotExist(e) {
			if err = s.MaterializeExport(out); err != nil {
				return ref, err
			}
		} else if err = s.VerifyExport(out, maxDomain); err != nil {
			return ref, err
		}
		return *cp.Lifecycle.FinalExport, nil
	}
	// Reuse an export left by a crash before checkpoint publication, but verify
	// its complete contents and beacon before marking it final.
	if _, e := os.Stat(out); os.IsNotExist(e) {
		if err = s.export(Core, out, beacon, maxDomain, &proof); err != nil {
			return ref, err
		}
	} else {
		var exported ExportManifest
		if err = ReadJSON(filepath.Join(out, "export.json"), &exported); err != nil {
			return ref, err
		}
		if exported.Beacon != hex.EncodeToString(beacon) || exported.BeaconProof == nil {
			return ref, fmt.Errorf("existing export has a different beacon")
		}
		if err = s.VerifyExport(out, maxDomain); err != nil {
			return ref, err
		}
	}
	var exported ExportManifest
	if err = ReadJSON(filepath.Join(out, "export.json"), &exported); err != nil {
		return ref, err
	}
	for _, c := range exported.Circuits {
		for _, suffix := range []string{".pk", ".vk"} {
			if _, err = s.put(filepath.Join(out, c.Circuit.Spec.ID+suffix)); err != nil {
				return ref, err
			}
		}
	}
	exportRef, err := s.put(filepath.Join(out, "export.json"))
	if err != nil {
		return ref, err
	}
	cp.Exports = append(cp.Exports, exportRef)
	cp.Lifecycle.Phase = "finalized"
	cp.Lifecycle.FinalExport = &exportRef
	if err = s.commitCheckpoint(cp, ref); err != nil {
		return Artifact{}, err
	}
	return exportRef, nil
}

func samePlan(a, b *BeaconPlan) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
