package ceremony

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/consensys/gnark-crypto/ecc"
	mpc "github.com/consensys/gnark/backend/groth16/bn254/mpcsetup"
	"github.com/consensys/gnark/frontend"
	"github.com/consensys/gnark/frontend/cs/r1cs"
)

type DemoCircuit struct {
	X              frontend.Variable
	Y              frontend.Variable `gnark:",public"`
	WithCommitment bool              `gnark:"-"`
}

func (c *DemoCircuit) Define(api frontend.API) error {
	api.AssertIsEqual(api.Mul(c.X, c.X), c.Y)
	if c.WithCommitment {
		v, e := api.(frontend.Committer).Commit(c.X)
		if e != nil {
			return e
		}
		api.AssertIsDifferent(v, 0)
	}
	return nil
}

func TestSRS(path string, power uint) error {
	if power < 2 || power > 22 {
		return fmt.Errorf("test SRS power must be between 2 and 22")
	}
	p := new(mpc.Phase1)
	p.Initialize(uint64(1) << power)
	p.Contribute()
	s, e := mpc.VerifyPhase1(uint64(1)<<power, []byte("SPR REHEARSAL ONLY - NOT A PUBLIC CEREMONY"), p)
	if e != nil {
		return e
	}
	if e = WriteBinary(path, &s); e != nil {
		return e
	}
	a, e := Digest(path)
	if e != nil {
		return e
	}
	return WriteJSON(path+".json", SRSInfo{Format: Format, Engine: Engine, Artifact: a, Provenance: "coordinator-generated rehearsal SRS; not production", InsecureTestSetup: true})
}

func Demo(dir string) error {
	tmp, e := os.MkdirTemp("", "spr-demo-*")
	if e != nil {
		return e
	}
	defer os.RemoveAll(tmp)
	var inputs []Input
	for _, item := range []struct {
		id, track string
		commit    bool
	}{{"demo_plain", Core, false}, {"demo_commitment", Core, true}, {"demo_large_track", Large, true}} {
		c, e := frontend.Compile(ecc.BN254.ScalarField(), r1cs.NewBuilder, &DemoCircuit{WithCommitment: item.commit})
		if e != nil {
			return e
		}
		path := filepath.Join(tmp, item.id+".r1cs")
		if e = WriteBinary(path, c); e != nil {
			return e
		}
		inputs = append(inputs, Input{Spec: Spec{ID: item.id, Track: item.track, Family: "rehearsal"}, R1CS: path})
	}
	srs := filepath.Join(tmp, "test.srs")
	if e = TestSRS(srs, 6); e != nil {
		return e
	}
	var info SRSInfo
	if e = ReadJSON(srs+".json", &info); e != nil {
		return e
	}
	return Prepare(dir, "rehearsal-only", srs, info, inputs, 64)
}
