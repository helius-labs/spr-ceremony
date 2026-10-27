package ceremony

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/consensys/gnark-crypto/ecc"
	"github.com/consensys/gnark-crypto/ecc/bn254"
	"github.com/consensys/gnark-crypto/ecc/bn254/mpcsetup"
	mpc "github.com/consensys/gnark/backend/groth16/bn254/mpcsetup"
	cs "github.com/consensys/gnark/constraint/bn254"
)

type Shape struct {
	Private     int   `json:"private"`
	Z           int   `json:"z"`
	Commitments []int `json:"commitments"`
}

func shapeOf(p *mpc.Phase2) Shape {
	s := Shape{Private: len(p.Parameters.G1.PKK), Z: len(p.Parameters.G1.Z)}
	for _, c := range p.Parameters.G1.SigmaCKK {
		s.Commitments = append(s.Commitments, len(c))
	}
	return s
}

// ValidatePhase2 checks compressed point tags and all length prefixes before
// gnark allocates slices. Accepted objects use gnark's canonical compressed format.
func ValidatePhase2(path string, s Shape) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	var nc uint16
	if e = binary.Read(r, binary.BigEndian, &nc); e != nil {
		return e
	}
	if int(nc) != len(s.Commitments) {
		return fmt.Errorf("commitment count mismatch")
	}
	point := func(size int) error {
		var b [64]byte
		if _, e := io.ReadFull(r, b[:size]); e != nil {
			return e
		}
		if b[0]&0xc0 == 0 {
			return fmt.Errorf("uncompressed point is not allowed")
		}
		return nil
	}
	vector := func(want int) error {
		var n uint32
		if e := binary.Read(r, binary.BigEndian, &n); e != nil {
			return e
		}
		if uint64(n) != uint64(want) {
			return fmt.Errorf("parameter vector size mismatch: got %d want %d", n, want)
		}
		for range n {
			if e := point(32); e != nil {
				return e
			}
		}
		return nil
	}
	if e = point(32); e != nil {
		return e
	}
	if e = vector(s.Private); e != nil {
		return e
	}
	if e = vector(s.Z); e != nil {
		return e
	}
	if e = point(64); e != nil {
		return e
	}
	for _, n := range s.Commitments {
		if e = vector(n); e != nil {
			return e
		}
	}
	for range s.Commitments {
		if e = point(64); e != nil {
			return e
		}
	}
	for i := 0; i < 1+len(s.Commitments); i++ {
		if e = point(32); e != nil {
			return e
		}
		if e = point(64); e != nil {
			return e
		}
	}
	n, e := r.ReadByte()
	if e != nil {
		return e
	}
	if n != 0 && n != 32 {
		return fmt.Errorf("invalid challenge length")
	}
	var b [32]byte
	if _, e = io.ReadFull(r, b[:n]); e != nil {
		return e
	}
	if _, e = r.ReadByte(); e != io.EOF {
		return fmt.Errorf("trailing phase2 data")
	}
	return nil
}

func ReadPhase2(path string, s Shape) (*mpc.Phase2, error) {
	if e := ValidatePhase2(path, s); e != nil {
		return nil, e
	}
	p := new(mpc.Phase2)
	if e := ReadBinary(path, p); e != nil {
		return nil, e
	}
	return p, nil
}

func Contribute(input, output string, s Shape) error {
	p, e := ReadPhase2(input, s)
	if e != nil {
		return e
	}
	p.Contribute()
	prev, e := ReadPhase2(input, s)
	if e != nil {
		return e
	}
	if e = prev.Verify(p); e != nil {
		return e
	}
	return WriteBinary(output, p)
}

func Verify(input, output string, s Shape) error {
	p, e := ReadPhase2(input, s)
	if e != nil {
		return e
	}
	n, e := ReadPhase2(output, s)
	if e != nil {
		return e
	}
	if len(n.Challenge) != 32 {
		return fmt.Errorf("contribution must bind its predecessor")
	}
	return p.Verify(n)
}

func ReadR1CS(path string) (*cs.R1CS, error) {
	c := new(cs.R1CS)
	if e := ReadBinary(path, c); e != nil {
		return nil, e
	}
	if c.GetNbConstraints() == 0 {
		return nil, fmt.Errorf("empty constraint system")
	}
	return c, nil
}

func Domain(c *cs.R1CS) uint64 { return ecc.NextPowerOfTwo(uint64(c.GetNbConstraints())) }

func ReadSRS(path string, maxDomain uint64) (*mpc.SrsCommons, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	var n uint64
	if e = binary.Read(f, binary.BigEndian, &n); e != nil {
		return nil, e
	}
	if n < 2 || n > maxDomain || ecc.NextPowerOfTwo(n) != n {
		return nil, fmt.Errorf("SRS domain %d exceeds limit %d or is invalid", n, maxDomain)
	}
	s := new(mpc.SrsCommons)
	if e = ReadBinary(path, s); e != nil {
		return nil, e
	}
	return s, nil
}

// trimSRS preserves the public Phase 1 powers for the circuit's FFT domain.
// The returned slices share immutable backing storage with the full SRS.
func trimSRS(full *mpc.SrsCommons, n uint64) (*mpc.SrsCommons, error) {
	if n < 2 || ecc.NextPowerOfTwo(n) != n || n > uint64(len(full.G2.Tau)) ||
		n > uint64(len(full.G1.AlphaTau)) || n > uint64(len(full.G1.BetaTau)) ||
		2*n-1 > uint64(len(full.G1.Tau)) {
		return nil, fmt.Errorf("invalid circuit SRS domain %d", n)
	}
	trim := *full
	trim.G1.Tau = full.G1.Tau[:2*n-1]
	trim.G1.AlphaTau = full.G1.AlphaTau[:n]
	trim.G1.BetaTau = full.G1.BetaTau[:n]
	trim.G2.Tau = full.G2.Tau[:n]
	return &trim, nil
}

func verifyDerivedSRS(full *mpc.SrsCommons, c Circuit) (*mpc.SrsCommons, error) {
	trim, err := trimSRS(full, c.Domain)
	if err != nil {
		return nil, err
	}
	h := hashWriter{}
	if _, err = trim.WriteTo(&h); err != nil {
		return nil, err
	}
	if h.artifact() != c.SRS {
		return nil, fmt.Errorf("%s SRS does not derive from the manifest's Phase 1 artifact", c.Spec.ID)
	}
	return trim, nil
}

// ImportPH1 bridges the old tool's compressed .ph1 encoding to gnark's common
// SRS. The caller must authenticate the file against independently verified
// Powers of Tau provenance; this verifies algebraic consistency, not provenance.
func ImportPH1(path, output, expected string, maxDomain uint64) error {
	a, e := Digest(path)
	if e != nil {
		return e
	}
	if a.SHA256 != expected {
		return fmt.Errorf("phase1 digest mismatch")
	}
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	var header [3]byte
	if _, e = io.ReadFull(r, header[:]); e != nil {
		return e
	}
	if header[0] > 28 {
		return fmt.Errorf("unsupported power")
	}
	n := uint64(1) << header[0]
	if n < 2 || n > maxDomain {
		return fmt.Errorf("phase1 domain %d exceeds limit %d", n, maxDomain)
	}
	s := new(mpc.SrsCommons)
	s.G1.Tau = make([]bn254.G1Affine, 2*n-1)
	s.G1.AlphaTau = make([]bn254.G1Affine, n)
	s.G1.BetaTau = make([]bn254.G1Affine, n)
	s.G2.Tau = make([]bn254.G2Affine, n)
	d := bn254.NewDecoder(r)
	for _, v := range [][]bn254.G1Affine{s.G1.Tau, s.G1.AlphaTau, s.G1.BetaTau} {
		for i := range v {
			if e = d.Decode(&v[i]); e != nil {
				return e
			}
		}
	}
	for i := range s.G2.Tau {
		if e = d.Decode(&s.G2.Tau[i]); e != nil {
			return e
		}
	}
	if e = d.Decode(&s.G2.Beta); e != nil {
		return e
	}
	_, _, g1, g2 := bn254.Generators()
	if !s.G1.Tau[0].Equal(&g1) || !s.G2.Tau[0].Equal(&g2) {
		return fmt.Errorf("invalid phase1 generators")
	}
	if s.G1.Tau[1].IsInfinity() || s.G1.Tau[1].Equal(&g1) {
		return fmt.Errorf("degenerate phase1 tau")
	}
	if s.G1.AlphaTau[0].IsInfinity() || s.G1.BetaTau[0].IsInfinity() || s.G2.Beta.IsInfinity() {
		return fmt.Errorf("zero phase1 parameters")
	}
	if e = mpcsetup.SameRatioMany(s.G1.Tau, s.G2.Tau, s.G1.AlphaTau, s.G1.BetaTau); e != nil {
		return e
	}
	ok, e := bn254.PairingCheck([]bn254.G1Affine{s.G1.BetaTau[0], func() bn254.G1Affine { var x bn254.G1Affine; x.Neg(&g1); return x }()}, []bn254.G2Affine{g2, s.G2.Beta})
	if e != nil {
		return e
	}
	if !ok {
		return fmt.Errorf("inconsistent beta")
	}
	return WriteBinary(output, s)
}
