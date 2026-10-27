// spr-browser-rehearsal builds public test bundles and verifies browser outputs.
// It cannot accept production parameters or submit to a ceremony coordinator.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"

	ceremony "github.com/helius-labs/spr-ceremony"
	"github.com/helius-labs/spr-ceremony/internal/browserproto"
)

var assets = []string{"index.html", "style.css", "app.js", "worker.js", "network.js", "wasm_exec.js", "contributor.wasm"}

type buildInfo struct {
	Format         string                       `json:"format"`
	Engine         string                       `json:"engine"`
	GoVersion      string                       `json:"go_version"`
	SourceRevision string                       `json:"source_revision"`
	SourceModified bool                         `json:"source_modified"`
	Assets         map[string]ceremony.Artifact `json:"assets"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: spr-browser-rehearsal <build-info|demo|prepare|serve|verify> [flags]")
	}
	f := flag.NewFlagSet(args[0], flag.ContinueOnError)
	dir := f.String("dir", "", "source rehearsal or prepared browser bundle")
	out := f.String("out", "", "new browser bundle directory")
	web := f.String("web-dir", "dist/browser", "assets produced by scripts/build-browser.sh")
	id := f.String("circuit", "demo_commitment", "one core circuit")
	listen := f.String("listen", "127.0.0.1:8081", "loopback preview address")
	contributed := f.String("contribution", "", "public browser contribution file")
	if err := f.Parse(args[1:]); err != nil {
		return err
	}
	if f.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	switch args[0] {
	case "build-info":
		if *out == "" {
			return fmt.Errorf("--out is required")
		}
		b := buildInfo{Format: "spr-browser-build-v1", Engine: ceremony.Engine, GoVersion: runtime.Version(), Assets: map[string]ceremony.Artifact{}}
		if info, ok := debug.ReadBuildInfo(); ok {
			for _, setting := range info.Settings {
				if setting.Key == "vcs.revision" {
					b.SourceRevision = setting.Value
				}
				if setting.Key == "vcs.modified" {
					b.SourceModified = setting.Value == "true"
				}
			}
		}
		for _, name := range assets {
			a, err := ceremony.Digest(filepath.Join(*out, name))
			if err != nil {
				return err
			}
			b.Assets[name] = a
		}
		return ceremony.WriteJSON(filepath.Join(*out, "build.json"), b)
	case "demo":
		tmp, err := os.MkdirTemp("", "spr-browser-demo-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(tmp)
		*dir = filepath.Join(tmp, "ceremony")
		if err = ceremony.Demo(*dir); err != nil {
			return err
		}
		return prepare(*dir, *id, *web, *out)
	case "prepare":
		return prepare(*dir, *id, *web, *out)
	case "serve":
		return serve(*dir, *listen)
	case "verify":
		var fixture browserproto.Fixture
		if err := ceremony.ReadJSON(filepath.Join(*dir, "fixture.json"), &fixture); err != nil {
			return err
		}
		if err := fixture.Validate(); err != nil {
			return err
		}
		input := filepath.Join(*dir, "input.ph2")
		if err := ceremony.Check(input, ceremony.Artifact(fixture.Input)); err != nil {
			return err
		}
		st, err := os.Stat(*contributed)
		if err != nil {
			return err
		}
		if !st.Mode().IsRegular() || st.Size() <= 0 || st.Size() > fixture.Input.Size+32 {
			return fmt.Errorf("invalid contribution size")
		}
		start := time.Now()
		if err = ceremony.Verify(input, *contributed, fixture.Shape); err != nil {
			return err
		}
		a, err := ceremony.Digest(*contributed)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"native_verification_passed": true, "seconds": time.Since(start).Seconds(), "circuit": fixture.Circuit, "input": fixture.Input, "output": a, "insecure_test_setup": true})
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}

func prepare(dir, id, web, out string) error {
	if dir == "" || out == "" {
		return fmt.Errorf("--dir and --out are required")
	}
	s, err := ceremony.Open(dir)
	if err != nil {
		return err
	}
	if !s.Manifest.Phase1.InsecureTestSetup {
		return fmt.Errorf("browser prototype requires explicitly insecure rehearsal parameters")
	}
	var selected *ceremony.Circuit
	for i := range s.Manifest.Circuits {
		c := &s.Manifest.Circuits[i]
		if c.Spec.ID == id && c.Spec.Track == ceremony.Core {
			selected = c
		}
	}
	if selected == nil {
		return fmt.Errorf("core circuit not found")
	}
	fixture := browserproto.Fixture{Format: browserproto.Format, Engine: ceremony.Engine, Track: ceremony.Core,
		Circuit: id, Manifest: browserproto.Artifact(s.ManifestArtifact), Input: browserproto.Artifact(selected.Initial),
		Shape: selected.Shape, InsecureTestSetup: true}
	if err = fixture.Validate(); err != nil {
		return err
	}
	if err = ceremony.Check(s.Object(selected.Initial), selected.Initial); err != nil {
		return err
	}
	var build buildInfo
	if err = ceremony.ReadJSON(filepath.Join(web, "build.json"), &build); err != nil {
		return err
	}
	if build.Format != "spr-browser-build-v1" || build.Engine != ceremony.Engine {
		return fmt.Errorf("browser assets use a different engine or format")
	}
	for _, name := range assets {
		if err = ceremony.Check(filepath.Join(web, name), build.Assets[name]); err != nil {
			return err
		}
	}
	if _, err = os.Lstat(out); !os.IsNotExist(err) {
		return fmt.Errorf("output must be a new directory")
	}
	if err = os.MkdirAll(out, 0700); err != nil {
		return err
	}
	copy := func(from, to string) error {
		f, err := os.Open(from)
		if err != nil {
			return err
		}
		defer f.Close()
		return ceremony.Atomic(to, func(w io.Writer) error { _, err := io.Copy(w, f); return err })
	}
	for _, name := range append(append([]string{}, assets...), "build.json") {
		if err = copy(filepath.Join(web, name), filepath.Join(out, name)); err != nil {
			return err
		}
	}
	if err = copy(s.Object(selected.Initial), filepath.Join(out, "input.ph2")); err != nil {
		return err
	}
	if err = ceremony.WriteJSON(filepath.Join(out, "fixture.json"), fixture); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"directory": out, "circuit": id, "input_bytes": selected.Initial.Size, "insecure_test_setup": true})
}

func serve(dir, address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil || (host != "localhost" && !net.ParseIP(host).IsLoopback()) {
		return fmt.Errorf("preview server must bind a loopback address; deploy the static bundle behind HTTPS for hosting")
	}
	if dir == "" {
		return fmt.Errorf("--dir is required")
	}
	var fixture browserproto.Fixture
	if err = ceremony.ReadJSON(filepath.Join(dir, "fixture.json"), &fixture); err != nil {
		return err
	}
	if err = fixture.Validate(); err != nil {
		return err
	}
	allowed := map[string]bool{"/fixture.json": true, "/input.ph2": true, "/build.json": true}
	for _, name := range assets {
		allowed["/"+name] = true
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	defer listener.Close()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			http.Error(w, "read-only rehearsal bundle", http.StatusMethodNotAllowed)
			return
		}
		path := r.URL.Path
		if path == "/" {
			path = "/index.html"
		}
		if !allowed[path] {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self' 'wasm-unsafe-eval'; worker-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		http.ServeFile(w, r, filepath.Join(dir, path[1:]))
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	if err = json.NewEncoder(os.Stdout).Encode(map[string]string{"url": "http://" + listener.Addr().String(), "mode": "rehearsal"}); err != nil {
		return err
	}
	err = server.Serve(listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
