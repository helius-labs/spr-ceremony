package browserproto

import (
	"strings"
	"testing"

	"github.com/helius-labs/spr-ceremony/internal/contribution"
)

func testFixture() Fixture {
	return Fixture{Format: Format, Engine: Engine, Track: "core", Circuit: "demo_plain",
		Manifest: Artifact{strings.Repeat("a", 64), 100}, Input: Artifact{strings.Repeat("b", 64), 267},
		Shape: contribution.Shape{Private: 1, Z: 1}, InsecureTestSetup: true}
}

func TestFixtureBounds(t *testing.T) {
	for _, mutate := range []func(*Fixture){
		func(f *Fixture) {},
		func(f *Fixture) { f.Input.Size += 32 },
		func(f *Fixture) { f.Shape.Commitments = []int{2}; f.Input.Size += 164 + 64 },
	} {
		f := testFixture()
		mutate(&f)
		if err := f.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	cases := map[string]func(*Fixture){
		"production":       func(f *Fixture) { f.InsecureTestSetup = false },
		"large-track":      func(f *Fixture) { f.Track = "batch-4999" },
		"wrong-engine":     func(f *Fixture) { f.Engine = "other" },
		"negative-length":  func(f *Fixture) { f.Shape.Private = -1 },
		"huge-length":      func(f *Fixture) { f.Shape.Z = int(^uint(0) >> 1) },
		"shape-mismatch":   func(f *Fixture) { f.Shape.Private++ },
		"too-many-commits": func(f *Fixture) { f.Shape.Commitments = make([]int, 256) },
		"bad-digest":       func(f *Fixture) { f.Input.SHA256 = "bad" },
		"too-large":        func(f *Fixture) { f.Input.Size = MaxInputBytes + 1 },
		"bad-id":           func(f *Fixture) { f.Circuit = "../other" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := testFixture()
			mutate(&f)
			if err := f.Validate(); err == nil {
				t.Fatal("accepted invalid browser fixture")
			}
		})
	}
}

func TestProductionEntryKeepsRehearsalGuard(t *testing.T) {
	f := Fixture{Format: ContributionFormat, Engine: Engine, Track: "core", Circuit: "merge_8_1", Manifest: Artifact{SHA256: strings.Repeat("a", 64), Size: 100}, Input: Artifact{SHA256: strings.Repeat("b", 64), Size: 203}}
	if err := f.ValidateContribution(); err != nil {
		t.Fatal(err)
	}
	if err := f.Validate(); err == nil {
		t.Fatal("rehearsal tool accepted production input")
	}
	f.Track = "batch-4999"
	if err := f.ValidateContribution(); err == nil {
		t.Fatal("large track accepted")
	}
	f.Track = "core"
	f.Input.Size = MaxInputBytes + 1
	if err := f.ValidateContribution(); err == nil {
		t.Fatal("oversized input accepted")
	}
}
