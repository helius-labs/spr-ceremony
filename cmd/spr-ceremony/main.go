package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	ceremony "github.com/helius-labs/spr-ceremony"
)

func main() {
	if e := run(os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, "error:", e)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: spr-ceremony <catalog|demo|test-srs|import-ph1|prepare|serve|invite|contribute|status|audit|export|verify-export> [flags]")
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	dir := f.String("dir", "ceremony-data", "coordinator ceremony directory")
	track := f.String("track", ceremony.Core, "core or batch-4999; large track is always opt-in")
	out := f.String("out", "", "output path (must be new for prepare/demo/export)")
	maxDomain := f.Uint64("max-domain", 1<<22, "maximum in-memory SRS domain; does not establish hardware suitability")
	var action func() error
	open := func() (*ceremony.Store, error) { return ceremony.Open(*dir) }
	print := func(v any) error { e := json.NewEncoder(os.Stdout); e.SetIndent("", "  "); return e.Encode(v) }
	requireOut := func() error {
		if *out == "" {
			return fmt.Errorf("--out is required")
		}
		return nil
	}
	switch args[0] {
	case "catalog":
		id := f.String("circuit", "", "select a single circuit")
		action = func() error {
			specs, e := ceremony.Select(*track, *id)
			if e != nil {
				return e
			}
			return print(specs)
		}
	case "demo":
		action = func() error {
			if e := requireOut(); e != nil {
				return e
			}
			if e := ceremony.Demo(*out); e != nil {
				return e
			}
			s, e := ceremony.Open(*out)
			if e != nil {
				return e
			}
			fmt.Fprintln(os.Stdout, "REHEARSAL manifest SHA256:", s.ManifestArtifact.SHA256)
			return nil
		}
	case "test-srs":
		power := f.Uint("power", 6, "domain power (rehearsal only)")
		action = func() error {
			if e := requireOut(); e != nil {
				return e
			}
			if _, e := os.Stat(*out); !os.IsNotExist(e) {
				return fmt.Errorf("output already exists or is inaccessible")
			}
			return ceremony.TestSRS(*out, *power)
		}
	case "import-ph1":
		input := f.String("input", "", "compressed semaphore-mtb-setup phase1 file")
		digest := f.String("sha256", "", "independently authenticated .ph1 SHA256")
		provenance := f.String("provenance", "", "record of verified public Powers of Tau origin and conversion")
		action = func() error {
			if e := requireOut(); e != nil {
				return e
			}
			if len(*digest) != 64 || *provenance == "" {
				return fmt.Errorf("--sha256 and --provenance are required")
			}
			if _, e := os.Stat(*out); !os.IsNotExist(e) {
				return fmt.Errorf("output already exists or is inaccessible")
			}
			if e := ceremony.ImportPH1(*input, *out, *digest, *maxDomain); e != nil {
				return e
			}
			a, e := ceremony.Digest(*out)
			if e != nil {
				return e
			}
			return ceremony.WriteJSON(*out+".json", ceremony.SRSInfo{Format: ceremony.Format, Engine: ceremony.Engine, Artifact: a, Provenance: *provenance})
		}
	case "prepare":
		r1cs := f.String("r1cs-dir", "", "directory produced by spr-zolana compile")
		srs := f.String("srs", "", "imported SRS path; adjacent .json metadata required")
		source := f.String("source-revision", "", "frozen Zolana source commit")
		id := f.String("circuit", "", "select one circuit")
		action = func() error {
			if e := requireOut(); e != nil {
				return e
			}
			specs, e := ceremony.Select(*track, *id)
			if e != nil {
				return e
			}
			var inputs []ceremony.Input
			for _, s := range specs {
				inputs = append(inputs, ceremony.Input{Spec: s, R1CS: filepath.Join(*r1cs, s.ID+".r1cs")})
			}
			var info ceremony.SRSInfo
			if e = ceremony.ReadJSON(*srs+".json", &info); e != nil {
				return e
			}
			return ceremony.Prepare(*out, *source, *srs, info, inputs, *maxDomain)
		}
	case "serve":
		listen := f.String("listen", "127.0.0.1:8080", "listen address; use a TLS reverse proxy outside localhost")
		action = func() error {
			s, e := open()
			if e != nil {
				return e
			}
			fmt.Fprintln(os.Stderr, "Serving", *listen, "manifest", s.ManifestArtifact.SHA256)
			server := &http.Server{Addr: *listen, Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 16 << 10}
			return server.ListenAndServe()
		}
	case "invite":
		base := f.String("url", "http://127.0.0.1:8080", "coordinator HTTPS origin")
		name := f.String("name", "", "contributor pseudonym (letters, digits, underscore, hyphen)")
		ttl := f.Duration("ttl", 24*time.Hour, "invitation lifetime")
		action = func() error {
			if e := requireOut(); e != nil {
				return e
			}
			if e := ceremony.ValidateURL(*base); e != nil {
				return e
			}
			s, e := open()
			if e != nil {
				return e
			}
			i, e := s.Invite(*base, *name, *track, *ttl)
			if e != nil {
				return e
			}
			if e = ceremony.WriteJSON(*out, i); e != nil {
				return e
			}
			fmt.Fprintf(os.Stdout, "Wrote invitation for %d %s circuits to %s\n", len(i.Jobs), i.Track, *out)
			return nil
		}
	case "contribute":
		invite := f.String("invitation", "", "private invitation JSON file")
		work := f.String("work", "work", "reusable local workspace")
		maxBytes := f.Int64("max-file-bytes", 1<<30, "maximum input file size; explicitly raise for large circuits")
		action = func() error {
			var i ceremony.Invitation
			if e := ceremony.ReadJSON(*invite, &i); e != nil {
				return e
			}
			c := ceremony.NewClient(*work)
			c.MaxFileBytes = *maxBytes
			return c.Run(context.Background(), i, *track)
		}
	case "status":
		action = func() error {
			s, e := open()
			if e != nil {
				return e
			}
			var states []ceremony.State
			for _, c := range s.Manifest.Circuits {
				if c.Spec.Track == *track {
					st, e := s.State(c.Spec.ID)
					if e != nil {
						return e
					}
					states = append(states, st)
				}
			}
			if len(states) == 0 {
				return fmt.Errorf("no circuits in track")
			}
			return print(states)
		}
	case "audit":
		digest := f.String("manifest-sha256", "", "out-of-band authenticated manifest digest")
		action = func() error {
			s, e := open()
			if e != nil {
				return e
			}
			if *digest != s.ManifestArtifact.SHA256 {
				return fmt.Errorf("--manifest-sha256 must match authenticated manifest")
			}
			if e = s.Audit(*track, *maxDomain); e != nil {
				return e
			}
			fmt.Fprintln(os.Stdout, "Transcript verified:", *track)
			return nil
		}
	case "export":
		beacon := f.String("beacon", "", "at least 32 bytes of hex public beacon material")
		action = func() error {
			if e := requireOut(); e != nil {
				return e
			}
			b, e := hex.DecodeString(*beacon)
			if e != nil {
				return e
			}
			s, e := open()
			if e != nil {
				return e
			}
			return s.Export(*track, *out, b, *maxDomain)
		}
	case "verify-export":
		keys := f.String("export", "", "export directory")
		digest := f.String("manifest-sha256", "", "out-of-band authenticated manifest digest")
		action = func() error {
			s, e := open()
			if e != nil {
				return e
			}
			if *digest != s.ManifestArtifact.SHA256 {
				return fmt.Errorf("manifest digest mismatch")
			}
			if e = s.VerifyExport(*keys, *maxDomain); e != nil {
				return e
			}
			fmt.Fprintln(os.Stdout, "Export verified against ceremony transcript")
			return nil
		}
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	return action()
}
