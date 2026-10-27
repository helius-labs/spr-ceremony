package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"
	"time"

	ceremony "github.com/helius-labs/spr-ceremony"
	"github.com/helius-labs/spr-ceremony/storage"
)

func main() {
	if e := run(os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, "error:", e)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: spr-ceremony <version|catalog|demo|test-srs|import-ph1|prepare|open|close|finalize|serve|invite|contribute|verify-contribution|verify-attestation|status|checkpoint|sync|restore|fetch-export|audit|export|verify-export> [flags]")
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	dir := f.String("dir", "ceremony-data", "coordinator ceremony directory")
	track := f.String("track", ceremony.Core, "core or batch-4999; large track is always opt-in")
	out := f.String("out", "", "output path (must be new for prepare/demo/export)")
	maxDomain := f.Uint64("max-domain", 1<<22, "maximum in-memory SRS domain; does not establish hardware suitability")
	remoteConfig := f.String("s3-config", "", "S3 configuration JSON (credentials use the AWS default chain)")
	var action func() error
	open := func() (*ceremony.Store, error) {
		s, e := ceremony.Open(*dir)
		if e != nil {
			return nil, e
		}
		path := *remoteConfig
		if path == "" {
			path = filepath.Join(*dir, "s3.json")
			if _, e = os.Stat(path); os.IsNotExist(e) {
				return s, nil
			}
		}
		var cfg storage.Config
		if e = ceremony.ReadJSON(path, &cfg); e != nil {
			return nil, e
		}
		s.Remote, e = storage.New(context.Background(), cfg)
		if e == nil && !s.Manifest.Phase1.InsecureTestSetup {
			e = s.Remote.(*storage.S3).CheckBucket(context.Background())
		}
		return s, e
	}
	print := func(v any) error { e := json.NewEncoder(os.Stdout); e.SetIndent("", "  "); return e.Encode(v) }
	requireOut := func() error {
		if *out == "" {
			return fmt.Errorf("--out is required")
		}
		return nil
	}
	switch args[0] {
	case "version":
		action = func() error {
			info, ok := debug.ReadBuildInfo()
			if !ok {
				return fmt.Errorf("build information unavailable")
			}
			return print(struct {
				Engine string           `json:"engine"`
				Build  *debug.BuildInfo `json:"build"`
			}{ceremony.Engine, info})
		}
	case "open":
		info := f.String("chain-info", "", "authenticated drand chain-info JSON")
		hash := f.String("chain-hash", "", "independently authenticated drand chain hash")
		round := f.Uint64("round", 0, "future drand round announced before opening")
		action = func() error {
			b, e := os.ReadFile(*info)
			if e != nil {
				return e
			}
			plan, e := ceremony.NewBeaconPlan(b, *hash, *round)
			if e != nil {
				return e
			}
			s, e := open()
			if e != nil {
				return e
			}
			ref, e := s.OpenCeremony(plan)
			if e != nil {
				return e
			}
			return print(ref)
		}
	case "close":
		action = func() error {
			s, e := open()
			if e != nil {
				return e
			}
			ref, e := s.CloseCeremony()
			if e != nil {
				return e
			}
			return print(ref)
		}
	case "finalize":
		proofFile := f.String("beacon-file", "", "JSON containing the planned signed beacon")
		proofURL := f.String("beacon-url", "", "HTTPS endpoint for the exact planned round")
		action = func() error {
			if e := requireOut(); e != nil {
				return e
			}
			s, e := open()
			if e != nil {
				return e
			}
			life, _, e := s.Lifecycle()
			if e != nil {
				return e
			}
			if life.Beacon == nil {
				return fmt.Errorf("ceremony has no beacon plan")
			}
			var proof ceremony.BeaconProof
			if *proofFile != "" && *proofURL != "" {
				return fmt.Errorf("choose one beacon source")
			}
			if *proofURL != "" {
				proof, e = ceremony.FetchBeacon(context.Background(), *proofURL, *life.Beacon)
			} else {
				e = ceremony.ReadJSON(*proofFile, &proof)
			}
			if e != nil {
				return e
			}
			ref, e := s.Finalize(*out, proof, *maxDomain)
			if e != nil {
				return e
			}
			return print(ref)
		}
	case "checkpoint", "sync":
		action = func() error {
			s, e := open()
			if e != nil {
				return e
			}
			_, ref, e := s.Lifecycle()
			if e != nil {
				return e
			}
			if args[0] == "sync" {
				ref, e = s.Sync(context.Background())
				if e != nil {
					return e
				}
				if remote, ok := s.Remote.(*storage.S3); ok {
					if e = ceremony.WriteJSON(filepath.Join(*dir, "s3.json"), remote.Config); e != nil {
						return e
					}
				}
			}
			point := ceremony.RecoveryPoint{Manifest: s.ManifestArtifact, Checkpoint: ref}
			if *out != "" {
				if e = ceremony.WriteJSON(*out, point); e != nil {
					return e
				}
				digest, e := ceremony.Digest(*out)
				if e != nil {
					return e
				}
				fmt.Fprintln(os.Stderr, "Recovery file SHA256:", digest.SHA256)
			}
			return print(point)
		}
	case "fetch-export":
		action = func() error {
			if e := requireOut(); e != nil {
				return e
			}
			s, e := open()
			if e != nil {
				return e
			}
			return s.MaterializeExport(*out)
		}
	case "restore":
		recovery := f.String("recovery-file", "", "authenticated checkpoint recovery JSON")
		digest := f.String("recovery-sha256", "", "out-of-band SHA256 of the recovery file")
		action = func() error {
			if e := requireOut(); e != nil {
				return e
			}
			a, e := ceremony.Digest(*recovery)
			if e != nil {
				return e
			}
			if a.SHA256 != *digest {
				return fmt.Errorf("recovery file digest mismatch")
			}
			var point ceremony.RecoveryPoint
			if e = ceremony.ReadJSON(*recovery, &point); e != nil {
				return e
			}
			var cfg storage.Config
			if e = ceremony.ReadJSON(*remoteConfig, &cfg); e != nil {
				return e
			}
			remote, e := storage.New(context.Background(), cfg)
			if e != nil {
				return e
			}
			if e = ceremony.Restore(context.Background(), remote, *out, point.Manifest, point.Checkpoint); e != nil {
				return e
			}
			fmt.Fprintln(os.Stdout, "Restored authenticated checkpoint; audit before resuming coordination.")
			return nil
		}
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
		maxUploads := f.Int("max-uploads", 2, "maximum concurrent staged contributions")
		leaseTTL := f.Duration("lease-ttl", 30*time.Minute, "renewable circuit reservation lifetime")
		reserve := f.Uint64("reserve-disk-bytes", 2<<30, "minimum free disk retained after staging an upload")
		action = func() error {
			s, e := open()
			if e != nil {
				return e
			}
			if s.Remote != nil {
				if _, e = s.Sync(context.Background()); e != nil {
					return e
				}
				if remote, ok := s.Remote.(*storage.S3); ok {
					if e = ceremony.WriteJSON(filepath.Join(*dir, "s3.json"), remote.Config); e != nil {
						return e
					}
				}
			}
			fmt.Fprintln(os.Stderr, "Serving", *listen, "manifest", s.ManifestArtifact.SHA256)
			server := &http.Server{Addr: *listen, Handler: s.HandlerWithOptions(ceremony.ServerOptions{MaxUploads: *maxUploads, LeaseTTL: *leaseTTL, UploadTimeout: 30 * time.Minute, MinFreeBytes: *reserve}), ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Minute, WriteTimeout: 2 * time.Hour, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 16 << 10}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			result := make(chan error, 1)
			go func() { result <- server.ListenAndServe() }()
			select {
			case e := <-result:
				return e
			case <-ctx.Done():
				shutdown, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				return server.Shutdown(shutdown)
			}
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
			i, e := s.InviteQueued(*base, *name, *track, *ttl)
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
	case "verify-contribution":
		invite := f.String("invitation", "", "authenticated invitation JSON; may be expired")
		work := f.String("work", "work", "retained participant workspace")
		action = func() error {
			if e := requireOut(); e != nil {
				return e
			}
			var i ceremony.Invitation
			if e := ceremony.ReadJSON(*invite, &i); e != nil {
				return e
			}
			if i.Track != *track {
				return fmt.Errorf("invitation track differs from --track")
			}
			a, e := ceremony.VerifyLocalContributions(*work, i)
			if e != nil {
				return e
			}
			if e = ceremony.WriteJSON(*out, a); e != nil {
				return e
			}
			digest, e := ceremony.Digest(*out)
			if e != nil {
				return e
			}
			fmt.Fprintln(os.Stderr, "Offline contribution verification passed. Public attestation SHA256:", digest.SHA256)
			return print(digest)
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
			life, ref, e := s.Lifecycle()
			if e != nil {
				return e
			}
			return print(struct {
				Lifecycle  ceremony.Lifecycle `json:"lifecycle"`
				Checkpoint ceremony.Artifact  `json:"checkpoint"`
				Circuits   []ceremony.State   `json:"circuits"`
			}{life, ref, states})
		}
	case "audit", "verify-attestation":
		attestation := f.String("attestation", "", "participant attestation JSON for inclusion verification")
		digest := f.String("manifest-sha256", "", "out-of-band authenticated manifest digest")
		checkpoint := f.String("checkpoint-sha256", "", "authenticated checkpoint digest (required for production)")
		action = func() error {
			s, e := open()
			if e != nil {
				return e
			}
			if *digest != s.ManifestArtifact.SHA256 {
				return fmt.Errorf("--manifest-sha256 must match authenticated manifest")
			}
			_, pinnedRef, e := s.Lifecycle()
			if e != nil {
				return e
			}
			s, e = s.Snapshot(pinnedRef)
			if e != nil {
				return e
			}
			if !s.Manifest.Phase1.InsecureTestSetup || *checkpoint != "" {
				_, ref, e := s.Lifecycle()
				if e != nil {
					return e
				}
				if ref.SHA256 != *checkpoint {
					return fmt.Errorf("--checkpoint-sha256 must match the authenticated publication")
				}
			}
			if args[0] == "verify-attestation" {
				var a ceremony.Attestation
				if e = ceremony.ReadJSON(*attestation, &a); e != nil {
					return e
				}
				if a.Track != *track {
					return fmt.Errorf("attestation track mismatch")
				}
				if e = s.VerifyAttestation(a); e != nil {
					return e
				}
				fmt.Fprintln(os.Stdout, "All attested receipts are included in checkpoint:", pinnedRef.SHA256)
				return nil
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
		checkpoint := f.String("checkpoint-sha256", "", "authenticated checkpoint digest (required for production)")
		action = func() error {
			s, e := open()
			if e != nil {
				return e
			}
			if *digest != s.ManifestArtifact.SHA256 {
				return fmt.Errorf("manifest digest mismatch")
			}
			_, pinnedRef, e := s.Lifecycle()
			if e != nil {
				return e
			}
			s, e = s.Snapshot(pinnedRef)
			if e != nil {
				return e
			}
			if !s.Manifest.Phase1.InsecureTestSetup || *checkpoint != "" {
				_, ref, e := s.Lifecycle()
				if e != nil {
					return e
				}
				if ref.SHA256 != *checkpoint {
					return fmt.Errorf("checkpoint digest mismatch")
				}
			}
			var metadata ceremony.ExportManifest
			if e = ceremony.ReadJSON(filepath.Join(*keys, "export.json"), &metadata); e != nil {
				return e
			}
			if metadata.Track != *track {
				return fmt.Errorf("export track does not match --track; batch-4999 requires explicit selection")
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
