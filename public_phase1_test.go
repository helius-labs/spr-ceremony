package ceremony

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/crypto/blake2b"
)

func TestPublicPhase1PinsMatchVerifiedEvidence(t *testing.T) {
	b, err := os.ReadFile("docs/evidence/public-phase1.json")
	must(t, err)
	var evidence struct {
		URL        string
		BLAKE2b    string
		PTAU       Artifact
		Conversion struct{ PH1 Artifact }
		Import     struct{ SRS Artifact }
	}
	must(t, json.Unmarshal(b, &evidence))
	p := publicPower21()
	if p.URL != evidence.URL || p.BLAKE2b != evidence.BLAKE2b || p.PTAU != evidence.PTAU ||
		p.PH1 != evidence.Conversion.PH1 || p.SRS != evidence.Import.SRS || p.Power != 21 {
		t.Fatal("public Phase 1 pins differ from the verified reference")
	}
}

type phase1Fixture struct {
	pins                  phase1Pins
	input, out            string
	conversions, imports  int
	convertErr, importErr error
	converted, imported   string
}

func newPhase1Fixture(t *testing.T) *phase1Fixture {
	t.Helper()
	ref := func(s string) Artifact { return Artifact{hashBytes([]byte(s)), int64(len(s))} }
	const input = "public transcript fixture"
	blake := blake2b.Sum512([]byte(input))
	f := &phase1Fixture{input: filepath.Join(t.TempDir(), "input.ptau"), out: filepath.Join(t.TempDir(), "phase1"),
		converted: "converted fixture", imported: "SRS fixture"}
	f.pins = phase1Pins{Power: 21, URL: "https://unused.invalid/public.ptau", BLAKE2b: hex.EncodeToString(blake[:]),
		PTAU: ref(input), PH1: ref(f.converted), SRS: ref(f.imported)}
	must(t, os.WriteFile(f.input, []byte(input), 0600))
	return f
}

func (f *phase1Fixture) run(ctx context.Context, input string) (SRSInfo, error) {
	return preparePublicPhase1(ctx, f.out, input, io.Discard, f.pins,
		func(_, output string) error {
			f.conversions++
			if err := os.WriteFile(output, []byte(f.converted), 0600); err != nil {
				return err
			}
			return f.convertErr
		},
		func(_, output, _ string, _ uint64) error {
			f.imports++
			if err := os.WriteFile(output, []byte(f.imported), 0600); err != nil {
				return err
			}
			return f.importErr
		})
}

func noPhase1Metadata(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"public-21.srs", "public-21.srs.json", "provenance.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("failed preparation published %s: %v", name, err)
		}
	}
}

func TestPublicPhase1AuthenticatesBeforeConversion(t *testing.T) {
	for _, mode := range []string{"truncated", "changed", "wrong-sha256", "wrong-blake2b"} {
		t.Run(mode, func(t *testing.T) {
			f := newPhase1Fixture(t)
			switch mode {
			case "truncated":
				must(t, os.WriteFile(f.input, []byte("short"), 0600))
			case "changed":
				must(t, os.WriteFile(f.input, []byte("Public transcript fixture"), 0600))
			case "wrong-sha256":
				f.pins.PTAU.SHA256 = strings.Repeat("0", 64)
			case "wrong-blake2b":
				f.pins.BLAKE2b = strings.Repeat("0", 128)
			}
			if _, err := f.run(context.Background(), f.input); err == nil {
				t.Fatal("accepted unauthenticated transcript")
			}
			if f.conversions != 0 || f.imports != 0 {
				t.Fatal("untrusted input reached the parser")
			}
			noPhase1Metadata(t, f.out)
		})
	}
}

func TestPublicPhase1FailedOutputCannotBePrepared(t *testing.T) {
	for _, mode := range []string{"conversion-error", "converted-hash", "import-error", "imported-hash"} {
		t.Run(mode, func(t *testing.T) {
			f := newPhase1Fixture(t)
			switch mode {
			case "conversion-error":
				f.convertErr = errors.New("conversion interrupted")
			case "converted-hash":
				f.converted = "incorrect conversion"
			case "import-error":
				f.importErr = errors.New("algebraic check failed")
			case "imported-hash":
				f.imported = "incorrect SRS"
			}
			if _, err := f.run(context.Background(), f.input); err == nil {
				t.Fatal("accepted failed conversion/import")
			}
			if strings.HasPrefix(mode, "conver") && f.imports != 0 {
				t.Fatal("incorrect conversion reached import")
			}
			noPhase1Metadata(t, f.out)
		})
	}
}

func TestPublicPhase1ResumeAndProvenance(t *testing.T) {
	f := newPhase1Fixture(t)
	f.importErr = errors.New("interrupted import")
	if _, err := f.run(context.Background(), f.input); err == nil {
		t.Fatal("expected interruption")
	}
	f.importErr = nil
	info, err := f.run(context.Background(), f.input)
	must(t, err)
	if f.conversions != 1 || f.imports != 2 || info.InsecureTestSetup || info.Artifact != f.pins.SRS {
		t.Fatal("resume did not reuse authenticated conversion")
	}
	var p publicPhase1Provenance
	must(t, ReadJSON(filepath.Join(f.out, "provenance.json"), &p))
	if p.HistoricalTranscriptReplayed || !p.AlgebraicChecksPassed || p.Mode != "pinned-public-transcript" {
		t.Fatal("provenance misrepresents the verification performed")
	}
	ref, err := Digest(filepath.Join(f.out, "provenance.json"))
	must(t, err)
	if !strings.Contains(info.Provenance, ref.SHA256) {
		t.Fatal("SRS does not bind provenance")
	}
	// A complete cache needs neither a network connection nor the original PTAU.
	must(t, os.Remove(f.input))
	again, err := f.run(context.Background(), "")
	must(t, err)
	if again != info || f.conversions != 1 || f.imports != 2 {
		t.Fatal("completed cache repeated preparation")
	}
	// Losing only the completion marker after a crash must be recoverable.
	must(t, os.Remove(filepath.Join(f.out, "public-21.srs.json")))
	must(t, os.WriteFile(f.input, []byte("public transcript fixture"), 0600))
	_, err = f.run(context.Background(), f.input)
	must(t, err)
	if f.conversions != 1 || f.imports != 3 {
		t.Fatal("incomplete SRS was not revalidated")
	}
}

func TestPublicPhase1RejectsCorruptCache(t *testing.T) {
	for _, name := range []string{"public-21.srs", "public-21.srs.json", "provenance.json"} {
		t.Run(name, func(t *testing.T) {
			f := newPhase1Fixture(t)
			_, err := f.run(context.Background(), f.input)
			must(t, err)
			must(t, os.WriteFile(filepath.Join(f.out, name), []byte("corrupted"), 0600))
			if _, err := f.run(context.Background(), ""); err == nil {
				t.Fatal("accepted corrupt cached parameters or metadata")
			}
		})
	}
	f := newPhase1Fixture(t)
	must(t, os.MkdirAll(f.out, 0700))
	must(t, os.WriteFile(filepath.Join(f.out, "public-21.ph1"), []byte("corrupt conversion"), 0600))
	if _, err := f.run(context.Background(), f.input); err == nil || f.imports != 0 {
		t.Fatal("corrupt partial cache reached import")
	}
}

func TestPublicPhase1ResumesDownload(t *testing.T) {
	f := newPhase1Fixture(t)
	data, err := os.ReadFile(f.input)
	must(t, err)
	var rangeHeader, authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader, authHeader = r.Header.Get("Range"), r.Header.Get("Authorization")
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 7-%d/%d", len(data)-1, len(data)))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(data[7:])
	}))
	defer server.Close()
	f.pins.URL = server.URL
	must(t, os.MkdirAll(f.out, 0700))
	input := filepath.Join(f.out, "powersOfTau28_hez_final_21.ptau")
	must(t, os.WriteFile(input+".part", data[:7], 0600))
	_, err = f.run(context.Background(), "")
	must(t, err)
	if rangeHeader != "bytes=7-" || authHeader != "" || f.conversions != 1 || f.imports != 1 {
		t.Fatalf("unexpected public download: range=%q auth=%q", rangeHeader, authHeader)
	}
	must(t, Check(input, f.pins.PTAU))
}

func TestPublicPhase1CancellationAndLock(t *testing.T) {
	f := newPhase1Fixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.run(ctx, f.input); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation: %v", err)
	}
	must(t, os.MkdirAll(f.out, 0700))
	lock, err := os.OpenFile(filepath.Join(f.out, "phase1.lock"), os.O_CREATE|os.O_RDWR, 0600)
	must(t, err)
	defer lock.Close()
	must(t, syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB))
	if _, err := f.run(context.Background(), f.input); err == nil || !strings.Contains(err.Error(), "locked") {
		t.Fatalf("concurrent preparation was not blocked: %v", err)
	}
	if f.conversions != 0 || f.imports != 0 {
		t.Fatal("cancelled/concurrent job performed cryptographic work")
	}
}
