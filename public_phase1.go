package ceremony

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/lightprotocol/ptau-deserializer/deserialize"
	"golang.org/x/crypto/blake2b"
)

type phase1Pins struct {
	Power   uint
	URL     string
	BLAKE2b string
	PTAU    Artifact
	PH1     Artifact
	SRS     Artifact
}

// These are the exact bytes verified with snarkjs 0.7.6 and the pinned legacy
// converter. See docs/evidence/public-phase1.json and its verification logs.
// Never accept caller-supplied hashes as replacements for these pins.
func publicPower21() phase1Pins {
	return phase1Pins{
		Power:   21,
		URL:     "https://circom.info/powersOfTau28_hez_final_21.ptau",
		BLAKE2b: "9aef0573cef4ded9c4a75f148709056bf989f80dad96876aadeb6f1c6d062391f07a394a9e756d16f7eb233198d5b69407cca44594c763ab4a5b67ae73254678",
		PTAU:    Artifact{"cdc7c94a6635bc91466d8c7d96faefe1d17ecc98a3596a748ca1e6c895f8c2b4", 2416002200},
		PH1:     Artifact{"ee243303f3207cb9ed86b836734938d55d56f8161a42afb7e51cdd143b34b43b", 402653219},
		SRS:     Artifact{"f75bd40496b046942e62cdd2e0a2c3a580c68f789e705e97fa8e3a9db7c36780", 402653128},
	}
}

type publicPhase1Provenance struct {
	Format                       string   `json:"format"`
	Mode                         string   `json:"mode"`
	Power                        uint     `json:"power"`
	Source                       string   `json:"source"`
	HashSource                   string   `json:"hash_source"`
	PublishedBLAKE2b             string   `json:"published_blake2b"`
	PTAU                         Artifact `json:"ptau"`
	Converter                    string   `json:"converter"`
	PH1                          Artifact `json:"ph1"`
	Engine                       string   `json:"engine"`
	SRS                          Artifact `json:"srs"`
	ReferenceVerification        string   `json:"reference_verification"`
	HistoricalTranscriptReplayed bool     `json:"historical_transcript_replayed"`
	AlgebraicChecksPassed        bool     `json:"algebraic_checks_passed"`
}

// PreparePublicPhase1 downloads and converts the fixed public power-21
// parameters. An optional input is an already downloaded copy of those exact
// bytes, not an arbitrary PTAU. The output directory is private, resumable, and
// must not be modified by other programs while this runs.
//
// This authenticates pinned artifacts and checks algebraic consistency. It does
// not replay the historical contribution transcript; verify-phase1.sh remains
// the separate, independent replay procedure. No ceremony is opened here.
func PreparePublicPhase1(ctx context.Context, dir, input string, progress io.Writer) (SRSInfo, error) {
	return preparePublicPhase1(ctx, dir, input, progress, publicPower21(), convertPublicPTAU, ImportPH1)
}

func preparePublicPhase1(ctx context.Context, dir, input string, progress io.Writer, pins phase1Pins,
	convert func(string, string) error, importPH1 func(string, string, string, uint64) error) (SRSInfo, error) {
	var empty SRSInfo
	if err := ctx.Err(); err != nil {
		return empty, err
	}
	if dir == "" {
		return empty, fmt.Errorf("Phase 1 output directory is required")
	}
	if progress == nil {
		progress = io.Discard
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return empty, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "phase1.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return empty, err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return empty, fmt.Errorf("Phase 1 directory is in use or cannot be locked: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	provenance := publicPhase1Provenance{
		Format: "spr-public-phase1-v1", Mode: "pinned-public-transcript", Power: pins.Power,
		Source: pins.URL, HashSource: "https://github.com/iden3/snarkjs/blob/master/README.md",
		PublishedBLAKE2b: pins.BLAKE2b, PTAU: pins.PTAU,
		Converter: "github.com/lightprotocol/ptau-deserializer@v0.2.1", PH1: pins.PH1,
		Engine: Engine, SRS: pins.SRS,
		ReferenceVerification:        "https://github.com/helius-labs/spr-ceremony/blob/fc76579fef1515fed4aac939bc8fa4043b13b435/docs/evidence/public-phase1.json",
		HistoricalTranscriptReplayed: false, AlgebraicChecksPassed: true,
	}
	b, err := json.MarshalIndent(provenance, "", "  ")
	if err != nil {
		return empty, err
	}
	b = append(b, '\n')
	provenanceRef := Artifact{hashBytes(b), int64(len(b))}
	info := SRSInfo{Format: Format, Engine: Engine, Artifact: pins.SRS,
		Provenance: "Pinned public power-21 parameters; historical transcript not replayed by this command; provenance.json SHA256 " + provenanceRef.SHA256}
	ph1 := filepath.Join(dir, "public-21.ph1")
	srs := filepath.Join(dir, "public-21.srs")
	provenancePath := filepath.Join(dir, "provenance.json")

	if input != "" {
		fmt.Fprintln(progress, "Authenticating the supplied public transcript (SHA256, BLAKE2b, size)...")
		if err = checkPublicPTAU(ctx, input, pins); err != nil {
			return empty, err
		}
	}
	if _, err = os.Lstat(srs + ".json"); err == nil {
		var cached SRSInfo
		if err = ReadJSON(srs+".json", &cached); err != nil {
			return empty, err
		}
		if cached != info {
			return empty, fmt.Errorf("cached Phase 1 metadata differs from the pinned source")
		}
		for path, ref := range map[string]Artifact{srs: pins.SRS, provenancePath: provenanceRef} {
			if err = Check(path, ref); err != nil {
				return empty, err
			}
		}
		fmt.Fprintln(progress, "Reusing authenticated Phase 1 parameters.")
		return info, ctx.Err()
	} else if !os.IsNotExist(err) {
		return empty, err
	}

	ph1Exists, err := checkedPhase1Cache(ph1, pins.PH1)
	if err != nil {
		return empty, err
	}
	if _, err = checkedPhase1Cache(srs, pins.SRS); err != nil {
		return empty, err
	}
	// Reserve room for the final SRS and, when necessary, the converted PH1 and
	// download. A partial SRS is recomputed before publishing completion metadata.
	required := uint64(pins.SRS.Size) + (64 << 20)
	if !ph1Exists {
		required += uint64(pins.PH1.Size)
	}
	if input == "" {
		input = filepath.Join(dir, "powersOfTau28_hez_final_21.ptau")
		exists, err := checkedPhase1Cache(input, pins.PTAU)
		if err != nil {
			return empty, err
		}
		if !exists {
			remaining := pins.PTAU.Size
			if st, err := os.Stat(input + ".part"); err == nil && st.Size() <= remaining {
				remaining -= st.Size()
			}
			if err = CheckDiskSpace(dir, required+uint64(remaining)); err != nil {
				return empty, fmt.Errorf("Phase 1 disk preflight: %w", err)
			}
			fmt.Fprintf(progress, "Downloading/resuming public power-21 transcript (%d bytes)...\n", pins.PTAU.Size)
			if err = NewClient(dir).download(ctx, pins.URL, input, pins.PTAU); err != nil {
				return empty, err
			}
		}
		fmt.Fprintln(progress, "Authenticating the public transcript (SHA256, BLAKE2b, size)...")
		if err = checkPublicPTAU(ctx, input, pins); err != nil {
			return empty, err
		}
	}
	if err = CheckDiskSpace(dir, required); err != nil {
		return empty, fmt.Errorf("Phase 1 disk preflight: %w", err)
	}
	if err = ctx.Err(); err != nil {
		return empty, err
	}
	tmp, err := os.MkdirTemp(dir, ".phase1-*")
	if err != nil {
		return empty, err
	}
	defer os.RemoveAll(tmp)
	if !ph1Exists {
		fmt.Fprintln(progress, "Converting authenticated public parameters...")
		staged := filepath.Join(tmp, "public-21.ph1")
		if err = convert(input, staged); err != nil {
			return empty, err
		}
		if err = Check(staged, pins.PH1); err != nil {
			return empty, fmt.Errorf("converted Phase 1 does not match the verified reference: %w", err)
		}
		if err = publishPhase1File(ctx, staged, ph1); err != nil {
			return empty, err
		}
	}
	if err = ctx.Err(); err != nil {
		return empty, err
	}
	fmt.Fprintln(progress, "Importing and checking curve, subgroup, and algebraic consistency; this may take several minutes...")
	staged := filepath.Join(tmp, "public-21.srs")
	if err = importPH1(ph1, staged, pins.PH1.SHA256, uint64(1)<<pins.Power); err != nil {
		return empty, err
	}
	if err = Check(staged, pins.SRS); err != nil {
		return empty, fmt.Errorf("imported SRS does not match the verified reference: %w", err)
	}
	if err = publishPhase1File(ctx, staged, srs); err != nil {
		return empty, err
	}
	if err = WriteJSON(provenancePath, provenance); err != nil {
		return empty, err
	}
	// This is the completion marker. No failed conversion/import produces metadata
	// that prepare can consume. Re-running validates the SRS and provenance again.
	if err = WriteJSON(srs+".json", info); err != nil {
		return empty, err
	}
	return info, nil
}

func checkedPhase1Cache(path string, expected Artifact) (bool, error) {
	st, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !st.Mode().IsRegular() || st.Size() != expected.Size {
		return false, fmt.Errorf("invalid cached Phase 1 file: %s", path)
	}
	return true, Check(path, expected)
}

type phase1ContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r phase1ContextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(b)
}

func checkPublicPTAU(ctx context.Context, path string, pins phase1Pins) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() || st.Size() != pins.PTAU.Size {
		return fmt.Errorf("public power-21 transcript size mismatch: %s", path)
	}
	sha := sha256.New()
	blake, _ := blake2b.New512(nil)
	n, err := io.Copy(io.MultiWriter(sha, blake), phase1ContextReader{ctx, io.LimitReader(f, pins.PTAU.Size+1)})
	if err != nil {
		return err
	}
	if n != pins.PTAU.Size || hex.EncodeToString(sha.Sum(nil)) != pins.PTAU.SHA256 || hex.EncodeToString(blake.Sum(nil)) != pins.BLAKE2b {
		return fmt.Errorf("public power-21 transcript checksum mismatch: %s", path)
	}
	return nil
}

// Only called after both fixed transcript hashes and its size have passed. The
// legacy parser is not suitable for arbitrary, unauthenticated PTAU files.
func convertPublicPTAU(input, output string) error {
	p, err := deserializer.InitPtau(input)
	if err != nil {
		return err
	}
	defer p.Close()
	if p.Header.Power != 21 {
		return fmt.Errorf("expected power-21 transcript")
	}
	return deserializer.WritePhase1FromPtauFile(p, output)
}

func publishPhase1File(ctx context.Context, from, to string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.OpenFile(from, os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	err = f.Sync()
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(from, to); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(to))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
