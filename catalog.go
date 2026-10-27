package ceremony

import (
	"fmt"
	"regexp"
)

const Format = "spr-ceremony-v1"
const Engine = "gnark-v0.16.4-bn254-mpcsetup"
const Core = "core"
const Large = "batch-4999"

var safeID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,99}$`)

type Spec struct {
	ID      string `json:"id"`
	Track   string `json:"track"`
	Family  string `json:"family"`
	Inputs  uint32 `json:"inputs,omitempty"`
	Outputs uint32 `json:"outputs,omitempty"`
	Height  uint32 `json:"height,omitempty"`
	Batch   uint32 `json:"batch,omitempty"`
}

// Catalog is the agreed production scope. No custom-ring or batch-10 circuits.
func Catalog() []Spec {
	shapes := [][2]uint32{{1, 2}, {1, 4}, {1, 8}, {2, 2}, {2, 4}, {1, 16}, {2, 8}, {3, 2}, {3, 4}, {2, 16}, {3, 8}, {4, 2}, {4, 4}, {4, 8}, {5, 2}, {5, 4}, {4, 16}, {5, 8}, {6, 2}, {6, 4}, {5, 16}, {6, 8}, {8, 2}, {8, 4}, {8, 8}, {8, 16}, {12, 2}, {12, 4}, {12, 8}, {16, 2}, {16, 4}, {16, 8}, {24, 2}, {24, 4}, {32, 2}, {40, 2}, {48, 2}, {49, 2}}
	var out []Spec
	for _, s := range shapes {
		out = append(out, Spec{ID: fmt.Sprintf("transfer_confidential_%d_%d", s[0], s[1]), Track: Core, Family: "transfer-confidential", Inputs: s[0], Outputs: s[1]})
	}
	for _, n := range []uint32{8, 24, 52} {
		out = append(out, Spec{ID: fmt.Sprintf("merge_%d_1", n), Track: Core, Family: "merge", Inputs: n, Outputs: 1})
	}
	return append(out, Spec{ID: "batch_address-append_40_4999", Track: Large, Family: "address-append", Height: 40, Batch: 4999})
}

func Select(track, id string) ([]Spec, error) {
	if track != Core && track != Large {
		return nil, fmt.Errorf("track must be %q or %q", Core, Large)
	}
	var result []Spec
	for _, s := range Catalog() {
		if s.Track == track && (id == "" || id == s.ID) {
			result = append(result, s)
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no circuits match track %q and circuit %q", track, id)
	}
	return result, nil
}

func IsCatalogSpec(spec Spec) bool {
	for _, s := range Catalog() {
		if spec == s {
			return true
		}
	}
	return false
}
