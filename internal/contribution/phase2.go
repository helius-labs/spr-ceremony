// Package contribution implements the same bounded Phase 2 contribution path
// for native clients and browser WebAssembly. It has no filesystem or network I/O.
package contribution

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"

	mpc "github.com/consensys/gnark/backend/groth16/bn254/mpcsetup"
)

type Shape struct {
	Private     int   `json:"private"`
	Z           int   `json:"z"`
	Commitments []int `json:"commitments"`
}

// Validate checks every compressed point tag and length before gnark allocates.
func Validate(input io.Reader, s Shape) error {
	var e error
	r := bufio.NewReaderSize(input, 1<<20)
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

// Read rewinds a stable, authenticated input before validation and decoding.
func Read(input io.ReadSeeker, s Shape) (*mpc.Phase2, error) {
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if err := Validate(input, s); err != nil {
		return nil, err
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	r := bufio.NewReaderSize(input, 1<<20)
	p := new(mpc.Phase2)
	if _, err := p.ReadFrom(r); err != nil {
		return nil, err
	}
	if _, err := r.ReadByte(); err != io.EOF {
		return nil, fmt.Errorf("trailing binary data")
	}
	return p, nil
}

// Contribute generates randomness through gnark's crypto/rand path, verifies
// the update against its input, and only then serializes the public result.
func Contribute(input io.ReadSeeker, output io.Writer, s Shape, progress func(string)) error {
	stage := func(name string) {
		if progress != nil {
			progress(name)
		}
	}
	stage("decode")
	p, err := Read(input, s)
	if err != nil {
		return err
	}
	stage("contribute")
	p.Contribute()
	stage("verify")
	prev, err := Read(input, s)
	if err != nil {
		return err
	}
	if err = prev.Verify(p); err != nil {
		return err
	}
	stage("encode")
	_, err = p.WriteTo(output)
	return err
}
