package ceremony

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var BrowserAssets = []string{"index.html", "style.css", "app.js", "worker.js", "network.js", "wasm_exec.js", "contributor.wasm", "hosted.html", "hosted.js", "protocol.js", "cache.js", "transfer.js", "session.js"}

type BrowserBuild struct {
	Format         string              `json:"format"`
	Engine         string              `json:"engine"`
	GoVersion      string              `json:"go_version"`
	SourceRevision string              `json:"source_revision"`
	SourceModified bool                `json:"source_modified"`
	Assets         map[string]Artifact `json:"assets"`
}

type WebOptions struct {
	Directory         string
	Origin            string
	DownloadOrigin    string
	NativeDirectory   string
	InsecureTestSetup bool
}

type nativeDownload struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
}

// WebHandler serves a checked, fixed set of assets and the coordinator API on
// one origin. It never serves the coordinator directory or exposes admin calls.
func WebHandler(api http.Handler, options WebOptions) (http.Handler, error) {
	if err := ValidateURL(options.Origin); err != nil {
		return nil, err
	}
	origin := strings.TrimRight(options.Origin, "/")
	if options.DownloadOrigin != "" {
		if err := ValidateURL(options.DownloadOrigin); err != nil {
			return nil, err
		}
		if !strings.HasPrefix(options.DownloadOrigin, "https://") && !options.InsecureTestSetup {
			return nil, fmt.Errorf("S3 download origin requires HTTPS")
		}
	}
	downloadOrigin := strings.TrimRight(options.DownloadOrigin, "/")
	var build BrowserBuild
	if err := ReadJSON(filepath.Join(options.Directory, "build.json"), &build); err != nil {
		return nil, err
	}
	if build.Format != "spr-browser-build-v1" || build.Engine != Engine {
		return nil, fmt.Errorf("browser bundle format or engine mismatch")
	}
	revision, revisionErr := hex.DecodeString(build.SourceRevision)
	if !options.InsecureTestSetup && (build.SourceModified || revisionErr != nil || len(revision) != 20) {
		return nil, fmt.Errorf("production hosting requires browser assets built from a clean committed checkout")
	}
	mux := http.NewServeMux()
	// Hold checked bytes in memory; changing files on disk cannot change a running
	// site's code. Restart deliberately to deploy a new checked bundle.
	for _, name := range BrowserAssets {
		ref, ok := build.Assets[name]
		if !ok || ref.Validate() != nil || ref.Size > 64<<20 {
			return nil, fmt.Errorf("missing or oversized browser asset: %s", name)
		}
		body, err := readAsset(filepath.Join(options.Directory, name), ref.Size)
		if err != nil {
			return nil, err
		}
		if int64(len(body)) != ref.Size || hashBytes(body) != ref.SHA256 {
			return nil, fmt.Errorf("browser asset checksum mismatch: %s", name)
		}
		// Rehearsal HTML/JS are not entry points on a live coordinator.
		if name == "index.html" || name == "app.js" {
			continue
		}
		path := "/" + name
		if name == "hosted.html" {
			path = "/{$}"
		}
		mux.HandleFunc("GET "+path, func(w http.ResponseWriter, r *http.Request) {
			if name == "worker.js" {
				// The computation worker has no access to S3 or any off-origin endpoint.
				w.Header().Set("Content-Security-Policy", browserCSP(""))
			}
			http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
		})
	}
	buildJSON, err := json.Marshal(build)
	if err != nil {
		return nil, err
	}
	mux.HandleFunc("GET /build.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(buildJSON)
	})
	downloads := []nativeDownload{}
	if options.NativeDirectory != "" {
		var native struct {
			SourceRevision string            `json:"source_revision"`
			Go             string            `json:"go"`
			SHA256         map[string]string `json:"sha256"`
		}
		if err = ReadJSON(filepath.Join(options.NativeDirectory, "build.json"), &native); err != nil {
			return nil, err
		}
		if native.SourceRevision != build.SourceRevision {
			return nil, fmt.Errorf("native binaries and browser bundle must use the same source revision")
		}
		for _, platform := range []string{"darwin-arm64", "darwin-amd64", "linux-arm64", "linux-amd64"} {
			name := "spr-ceremony-" + platform
			path := filepath.Join(options.NativeDirectory, name)
			ref, e := Digest(path)
			if e != nil {
				return nil, e
			}
			if native.SHA256[name] != ref.SHA256 || ref.Size > 128<<20 {
				return nil, fmt.Errorf("native binary checksum mismatch: %s", name)
			}
			body, e := readAsset(path, ref.Size)
			if e != nil {
				return nil, e
			}
			if hashBytes(body) != ref.SHA256 {
				return nil, fmt.Errorf("native binary changed during startup")
			}
			downloads = append(downloads, nativeDownload{Name: name, URL: "/native/" + name, SHA256: ref.SHA256})
			mux.HandleFunc("GET /native/"+name, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
				http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(body))
			})
		}
	}
	mux.HandleFunc("GET /ui-config.json", func(w http.ResponseWriter, r *http.Request) {
		respond(w, map[string]any{"format": "spr-hosted-ui-v1", "origin": origin, "download_origin": downloadOrigin, "native": downloads})
	})
	mux.Handle("/v1/", api)
	mux.Handle("/healthz", api)
	parsed, _ := url.Parse(origin)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", browserCSP(downloadOrigin))
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
		w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		if parsed.Scheme == "https" {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}
		if !strings.EqualFold(r.Host, parsed.Host) {
			http.Error(w, "unexpected host", 421)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && o != origin {
			http.Error(w, "cross-origin request rejected", 403)
			return
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" && strings.HasPrefix(r.URL.Path, "/v1/") {
			http.Error(w, "cross-site API request rejected", 403)
			return
		}
		mux.ServeHTTP(w, r)
	}), nil
}

func readAsset(path string, size int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() != size {
		return nil, fmt.Errorf("asset size mismatch: %s", filepath.Base(path))
	}
	return io.ReadAll(io.LimitReader(f, size+1))
}

func browserCSP(downloadOrigin string) string {
	connect := "'self'"
	if downloadOrigin != "" {
		connect += " " + downloadOrigin
	}
	return "default-src 'none'; script-src 'self' 'wasm-unsafe-eval'; worker-src 'self'; style-src 'self'; connect-src " + connect + "; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'"
}
