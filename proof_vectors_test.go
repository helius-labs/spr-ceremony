package ceremony

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	groth16 "github.com/consensys/gnark/backend/groth16/bn254"
)

// These saved proofs were produced by the previous engine revision. Verify them
// directly so dependency upgrades cannot silently break published key encodings
// or proofs while passing tests that generate fresh fixtures with the new code.
func TestPublishedCoreProofVectors(t *testing.T) {
	specs, err := Select(Core, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range specs {
		t.Run(spec.ID, func(t *testing.T) {
			var vector struct {
				ID         string     `json:"id"`
				VK         []byte     `json:"vk"`
				A          []byte     `json:"a"`
				B          []byte     `json:"b"`
				C          []byte     `json:"c"`
				Public     [][32]byte `json:"public"`
				Commitment []byte     `json:"commitment,omitempty"`
				PoK        []byte     `json:"pok,omitempty"`
			}
			if err := ReadJSON(filepath.Join("docs", "evidence", "core-vectors", spec.ID+".json"), &vector); err != nil {
				t.Fatal(err)
			}
			if vector.ID != spec.ID || len(vector.Public) == 0 {
				t.Fatal("invalid proof vector identity or public inputs")
			}
			var vk groth16.VerifyingKey
			r := bytes.NewReader(vector.VK)
			if _, err := vk.ReadFrom(r); err != nil {
				t.Fatal(err)
			}
			if r.Len() != 0 {
				t.Fatal("trailing verification-key data")
			}
			point := func(dst interface{ SetBytes([]byte) (int, error) }, data []byte) {
				t.Helper()
				n, err := dst.SetBytes(data)
				if err != nil || n != len(data) {
					t.Fatalf("decode proof point: read %d/%d, error %v", n, len(data), err)
				}
			}
			var proof groth16.Proof
			point(&proof.Ar, vector.A)
			point(&proof.Bs, vector.B)
			point(&proof.Krs, vector.C)
			if len(vector.Commitment) > 0 {
				proof.Commitments = make([]bn254.G1Affine, 1)
				point(&proof.Commitments[0], vector.Commitment)
				point(&proof.CommitmentPok, vector.PoK)
			} else if len(vector.PoK) > 0 {
				t.Fatal("commitment proof without commitment")
			}
			public := make(fr.Vector, len(vector.Public))
			for i := range public {
				if err := public[i].SetBytesCanonical(vector.Public[i][:]); err != nil {
					t.Fatal(err)
				}
			}
			if err := groth16.Verify(&proof, &vk, public); err != nil {
				t.Fatal(err)
			}
			one := fr.One()
			public[0].Add(&public[0], &one)
			if err := groth16.Verify(&proof, &vk, public); err == nil {
				t.Fatal("proof accepted with a changed public input")
			}
		})
	}
}
