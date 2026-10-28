package ceremony

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Handler exposes public artifacts/status and authenticated contribution uploads.
// Run behind HTTPS outside localhost. It intentionally provides no admin API.
func (s *Store) Handler() http.Handler {
	return s.HandlerWithOptions(ServerOptions{MaxUploads: 2, LeaseTTL: 30 * time.Minute, UploadTimeout: 30 * time.Minute, MinFreeBytes: 2 << 30})
}

type ServerOptions struct {
	MaxUploads    int
	LeaseTTL      time.Duration
	UploadTimeout time.Duration
	MinFreeBytes  uint64
}
type DownloadTicket struct {
	URL      string   `json:"url"`
	Artifact Artifact `json:"artifact"`
}

func (s *Store) HandlerWithOptions(options ServerOptions) http.Handler {
	if options.MaxUploads < 1 {
		options.MaxUploads = 1
	}
	if options.LeaseTTL <= 0 {
		options.LeaseTTL = 30 * time.Minute
	}
	if options.UploadTimeout <= 0 {
		options.UploadTimeout = 30 * time.Minute
	}
	uploads := make(chan struct{}, options.MaxUploads)
	var active sync.Map
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/ceremony", func(w http.ResponseWriter, r *http.Request) {
		view, err := s.PublicView()
		if err != nil {
			http.Error(w, "Ceremony information unavailable", 503)
			return
		}
		respond(w, view)
	})
	mux.HandleFunc("GET /v1/invitation", func(w http.ResponseWriter, r *http.Request) {
		i, err := s.invitation(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		w.Header().Set("Cache-Control", "no-store")
		if err != nil {
			http.Error(w, "invalid or expired invitation", 401)
			return
		}
		respond(w, i)
	})
	mux.HandleFunc("POST /v1/release/{circuit}", func(w http.ResponseWriter, r *http.Request) {
		err := s.Release(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), r.PathValue("circuit"))
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		respond(w, map[string]bool{"released": true})
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if _, _, err := s.checkpoint(); err != nil {
			http.Error(w, "checkpoint unavailable", 503)
			return
		}
		respond(w, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /v1/checkpoint", func(w http.ResponseWriter, r *http.Request) {
		cp, ref, err := s.checkpoint()
		if err != nil {
			http.Error(w, "checkpoint unavailable", 503)
			return
		}
		respond(w, struct {
			Ref        Artifact   `json:"ref"`
			Checkpoint Checkpoint `json:"checkpoint"`
		}{ref, cp})
	})
	mux.HandleFunc("POST /v1/claim", func(w http.ResponseWriter, r *http.Request) {
		claim, err := s.Claim(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), options.LeaseTTL)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		respond(w, claim)
	})
	mux.HandleFunc("POST /v1/heartbeat/{circuit}", func(w http.ResponseWriter, r *http.Request) {
		claim, err := s.Renew(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), r.PathValue("circuit"), options.LeaseTTL)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		respond(w, claim)
	})
	mux.HandleFunc("GET /v1/download/{hash}", func(w http.ResponseWriter, r *http.Request) {
		h := r.PathValue("hash")
		a := Artifact{SHA256: h, Size: 1}
		if a.Validate() != nil {
			http.NotFound(w, r)
			return
		}
		st, err := os.Stat(s.Object(a))
		if err != nil || !st.Mode().IsRegular() {
			http.NotFound(w, r)
			return
		}
		a.Size = st.Size()
		url := "/v1/objects/" + h
		if s.Remote != nil {
			url, err = s.Remote.DownloadURL(r.Context(), objectKey(a), 15*time.Minute)
			if err != nil {
				http.Error(w, "artifact download unavailable", 503)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		respond(w, DownloadTicket{URL: url, Artifact: a})
	})
	mux.HandleFunc("GET /v1/manifest", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		http.ServeFile(w, r, s.Dir+"/manifest.json")
	})
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) {
		var states []State
		for _, c := range s.Manifest.Circuits {
			st, e := s.State(c.Spec.ID)
			if e != nil {
				http.Error(w, "cannot read circuit state", 500)
				return
			}
			states = append(states, st)
		}
		respond(w, states)
	})
	mux.HandleFunc("GET /v1/objects/{hash}", func(w http.ResponseWriter, r *http.Request) {
		h := r.PathValue("hash")
		if len(h) != 64 || strings.ContainsAny(h, "/\\.") {
			http.NotFound(w, r)
			return
		}
		// Object names are SHA256 hex; never serve arbitrary filesystem paths.
		for _, c := range h {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				http.NotFound(w, r)
				return
			}
		}
		w.Header().Set("ETag", `"`+h+`"`)
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		http.ServeFile(w, r, s.Object(Artifact{SHA256: h}))
	})
	mux.HandleFunc("POST /v1/contributions/{circuit}", func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		i, e := s.invitation(token)
		if e != nil {
			http.Error(w, "invalid or expired invitation", 401)
			return
		}
		id := r.PathValue("circuit")
		var job *Job
		for j := range i.Jobs {
			if i.Jobs[j].Circuit.Spec.ID == id {
				job = &i.Jobs[j]
				break
			}
		}
		if job == nil {
			http.Error(w, "circuit is not in invitation", 403)
			return
		}
		if i.Mode == "queue" {
			if lease, err := s.lease(token, id); err == nil {
				job = &lease.Job
			}
		}
		maxSize := job.Input.Size + 1024
		if r.ContentLength <= 0 || r.ContentLength > maxSize {
			http.Error(w, "invalid contribution length", 413)
			return
		}
		select {
		case uploads <- struct{}{}:
			defer func() { <-uploads }()
		default:
			w.Header().Set("Retry-After", "5")
			http.Error(w, "upload capacity reached", 429)
			return
		}
		owner := hashBytes([]byte(token))
		if _, loaded := active.LoadOrStore(owner, true); loaded {
			w.Header().Set("Retry-After", "5")
			http.Error(w, "contribution already in progress", 429)
			return
		}
		defer active.Delete(owner)
		if err := CheckDiskSpace(s.Dir, 2*uint64(r.ContentLength)+options.MinFreeBytes); err != nil {
			http.Error(w, "insufficient coordinator disk capacity", 507)
			return
		}
		if err := http.NewResponseController(w).SetReadDeadline(time.Now().Add(options.UploadTimeout)); err != nil {
			http.Error(w, "cannot bound upload duration", 500)
			return
		}
		f, e := os.CreateTemp(s.Dir, ".upload-*")
		if e != nil {
			http.Error(w, "cannot stage upload", 500)
			return
		}
		path := f.Name()
		defer os.Remove(path)
		n, e := io.Copy(f, http.MaxBytesReader(w, r.Body, maxSize))
		closeErr := f.Close()
		if e != nil || closeErr != nil || n != r.ContentLength {
			http.Error(w, "incomplete upload", 400)
			return
		}
		_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
		receipt, e := s.Accept(token, id, path)
		if e != nil {
			if errors.Is(e, ErrPublicationUnavailable) && !errors.Is(e, ErrRemoteConflict) {
				http.Error(w, "checkpoint publication unavailable; retry this contribution", 503)
				return
			}
			http.Error(w, e.Error(), 409)
			return
		}
		respond(w, receipt)
	})
	return mux
}
func respond(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}
