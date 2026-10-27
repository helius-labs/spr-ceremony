package ceremony

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
)

// Handler exposes public artifacts/status and authenticated contribution uploads.
// Run behind HTTPS outside localhost. It intentionally provides no admin API.
func (s *Store) Handler() http.Handler {
	mux := http.NewServeMux()
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
		maxSize := job.Input.Size + 1024
		if r.ContentLength <= 0 || r.ContentLength > maxSize {
			http.Error(w, "invalid contribution length", 413)
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
		receipt, e := s.Accept(token, id, path)
		if e != nil {
			http.Error(w, e.Error(), 409)
			return
		}
		respond(w, receipt)
	})
	return mux
}
func respond(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
