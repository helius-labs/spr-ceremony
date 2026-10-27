package ceremony

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
)

const maxJSONBytes = 4 << 20

type Artifact struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func Digest(path string) (Artifact, error) {
	f, e := os.Open(path)
	if e != nil {
		return Artifact{}, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	return Artifact{hex.EncodeToString(h.Sum(nil)), n}, e
}

func (a Artifact) Validate() error {
	b, e := hex.DecodeString(a.SHA256)
	if e != nil || len(b) != 32 || a.Size <= 0 {
		return fmt.Errorf("invalid artifact reference")
	}
	return nil
}

func Check(path string, a Artifact) error {
	if e := a.Validate(); e != nil {
		return e
	}
	got, e := Digest(path)
	if e != nil {
		return e
	}
	if got != a {
		return fmt.Errorf("artifact hash or size mismatch: %s", path)
	}
	return nil
}

// Atomic writes flush and sync before renaming. A failed computation never
// leaves a reusable output file. All callers use private directories.
func Atomic(path string, write func(io.Writer) error) error {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".pending-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	b := bufio.NewWriterSize(f, 1<<20)
	if e = write(b); e != nil {
		return e
	}
	if e = b.Flush(); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	defer d.Close()
	return d.Sync()
}

func WriteJSON(path string, v any) error {
	return Atomic(path, func(w io.Writer) error { enc := json.NewEncoder(w); enc.SetIndent("", "  "); return enc.Encode(v) })
}
func ReadJSON(path string, v any) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return e
	}
	if st.Size() > maxJSONBytes {
		return fmt.Errorf("JSON metadata exceeds 4 MiB limit")
	}
	return decodeJSON(io.LimitReader(f, maxJSONBytes), v)
}
func decodeJSON(r io.Reader, v any) error {
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	var extra any
	if e := d.Decode(&extra); e != io.EOF {
		return fmt.Errorf("unexpected trailing JSON")
	}
	return nil
}

func WriteBinary(path string, v io.WriterTo) error {
	return Atomic(path, func(w io.Writer) error { _, e := v.WriteTo(w); return e })
}
func ReadBinary(path string, v io.ReaderFrom) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	if _, e = v.ReadFrom(r); e != nil {
		return e
	}
	if _, e = r.ReadByte(); e != io.EOF {
		return fmt.Errorf("trailing binary data")
	}
	return nil
}

func hashBytes(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

type hashWriter struct {
	h hash.Hash
	n int64
}

func (w *hashWriter) Write(b []byte) (int, error) {
	if w.h == nil {
		w.h = sha256.New()
	}
	n, e := w.h.Write(b)
	w.n += int64(n)
	return n, e
}
func (w *hashWriter) artifact() Artifact { return Artifact{hex.EncodeToString(w.h.Sum(nil)), w.n} }
