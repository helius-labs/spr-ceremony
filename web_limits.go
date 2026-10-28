package ceremony

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Bound public HTTP work and per-peer bursts. This is an application guard,
// not protection against a distributed network attack. Local health checks use
// the same limits; invitation authentication still gates all computation.
func limitWeb(next http.Handler, trustLoopbackProxy bool) http.Handler {
	type bucket struct {
		tokens  float64
		updated time.Time
	}
	var mu sync.Mutex
	peers := map[string]bucket{}
	requests := make(chan struct{}, 64)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		peer := net.ParseIP(host)
		if trustLoopbackProxy && peer != nil && peer.IsLoopback() {
			// Caddy appends the actual peer; never trust the user-controlled first
			// address in a forwarded chain or any header on a direct connection.
			parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
			if ip := net.ParseIP(strings.TrimSpace(parts[len(parts)-1])); ip != nil {
				peer = ip
			}
		}
		key := peer.String()
		now := time.Now()
		mu.Lock()
		b, exists := peers[key]
		if !exists && len(peers) >= 4096 {
			for k, value := range peers {
				if now.Sub(value.updated) > time.Minute {
					delete(peers, k)
				}
			}
		}
		allowed := exists || len(peers) < 4096
		if allowed {
			if !exists {
				b.tokens = 240
			} else {
				b.tokens = min(240, b.tokens+now.Sub(b.updated).Seconds()*20)
			}
			b.updated = now
			allowed = b.tokens >= 1
			if allowed {
				b.tokens--
			}
			peers[key] = b
		}
		mu.Unlock()
		if !allowed {
			w.Header().Set("Retry-After", "5")
			http.Error(w, "Too many requests. Retry shortly.", 429)
			return
		}
		select {
		case requests <- struct{}{}:
			defer func() { <-requests }()
		default:
			w.Header().Set("Retry-After", "5")
			http.Error(w, "Coordinator busy. Retry shortly.", 503)
			return
		}
		next.ServeHTTP(w, r)
	})
}
