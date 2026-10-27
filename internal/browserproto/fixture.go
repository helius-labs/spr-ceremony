// Package browserproto defines the bounded, rehearsal-only browser prototype.
package browserproto

import (
	"encoding/hex"
	"fmt"
	"regexp"

	"github.com/helius-labs/spr-ceremony/internal/contribution"
)

const Format = "spr-browser-rehearsal-v1"
const ContributionFormat = "spr-browser-contribution-v1"
const Engine = "gnark-v0.16.4-bn254-mpcsetup"
const MaxInputBytes = 128 << 20

type Artifact struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Fixture struct {
	Format            string             `json:"format"`
	Engine            string             `json:"engine"`
	Track             string             `json:"track"`
	Circuit           string             `json:"circuit"`
	Manifest          Artifact           `json:"manifest"`
	Input             Artifact           `json:"input"`
	Shape             contribution.Shape `json:"shape"`
	InsecureTestSetup bool               `json:"insecure_test_setup"`
}

func (f Fixture) Validate() error {
	if f.Format != Format || f.Engine != Engine || f.Track != "core" || !f.InsecureTestSetup {
		return fmt.Errorf("browser prototype accepts marked core rehearsals only")
	}
	return f.validateBounds()
}

// ValidateContribution is a separate entry point: rehearsal tools retain their
// test-only guard. The hosted client authenticates this job against its pinned
// manifest and queue reservation before handing it to the isolated engine.
func (f Fixture) ValidateContribution() error {
	if f.Format != ContributionFormat || f.Engine != Engine || f.Track != "core" {
		return fmt.Errorf("browser contributions require the pinned core engine")
	}
	return f.validateBounds()
}

func (f Fixture) validateBounds() error {
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,99}$`).MatchString(f.Circuit) {
		return fmt.Errorf("invalid circuit ID")
	}
	for _, a := range []Artifact{f.Input, f.Manifest} {
		b, err := hex.DecodeString(a.SHA256)
		if err != nil || len(b) != 32 || a.Size <= 0 {
			return fmt.Errorf("invalid artifact reference")
		}
	}
	if f.Input.Size > MaxInputBytes || len(f.Shape.Commitments) > 255 {
		return fmt.Errorf("fixture exceeds browser prototype limits")
	}
	// The serialized size must agree with the authenticated vector lengths.
	// This bounds every gnark allocation even for malicious fixture metadata.
	n := int64(203)
	for _, size := range append([]int{f.Shape.Private, f.Shape.Z}, f.Shape.Commitments...) {
		if size < 0 || size > MaxInputBytes/32 {
			return fmt.Errorf("invalid parameter vector size")
		}
		n += 32 * int64(size)
	}
	n += 164 * int64(len(f.Shape.Commitments))
	if f.Input.Size != n && f.Input.Size != n+32 {
		return fmt.Errorf("fixture shape and input size disagree")
	}
	return nil
}
