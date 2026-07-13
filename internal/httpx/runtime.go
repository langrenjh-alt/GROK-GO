package httpx

import (
	"net/http"
	"strings"
	"sync/atomic"
)

// DynamicCORS reads the current allowlist on every request, allowing a
// validated runtime settings snapshot to take effect without rebuilding the
// router. It intentionally does not enable credentialed cross-origin admin
// sessions.
func DynamicCORS(origins func() string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.TrimRight(strings.TrimSpace(r.Header.Get("Origin")), "/")
			if origin == "" || origins == nil {
				next.ServeHTTP(w, r)
				return
			}
			allowed, wildcard := originAllowed(origin, origins())
			w.Header().Add("Vary", "Origin")
			if !allowed {
				if isPreflight(r) {
					WriteErrorForRequest(w, r, http.StatusForbidden, "cors_origin_denied", "The request origin is not allowed.")
					return
				}
				next.ServeHTTP(w, r)
				return
			}
			if wildcard {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			} else {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			}
			w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID, Retry-After, X-RateLimit-Limit-Requests, X-RateLimit-Remaining-Requests")
			if isPreflight(r) {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-API-Key, Anthropic-Version, Anthropic-Beta")
				w.Header().Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func originAllowed(origin, configured string) (allowed, wildcard bool) {
	for _, candidate := range strings.Split(configured, ",") {
		candidate = strings.TrimRight(strings.TrimSpace(candidate), "/")
		if candidate == "*" {
			return true, true
		}
		if candidate == origin {
			return true, false
		}
	}
	return false, false
}

func isPreflight(r *http.Request) bool {
	return r.Method == http.MethodOptions && strings.TrimSpace(r.Header.Get("Access-Control-Request-Method")) != ""
}

// DynamicConcurrency applies a process-wide in-flight request limit. A limit
// below one disables the guard; validated administrator settings are positive.
func DynamicConcurrency(limit func() int) func(http.Handler) http.Handler {
	var active atomic.Int64
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			maximum := 0
			if limit != nil {
				maximum = limit()
			}
			if maximum <= 0 {
				next.ServeHTTP(w, r)
				return
			}
			current := active.Add(1)
			if current > int64(maximum) {
				active.Add(-1)
				w.Header().Set("Retry-After", "1")
				WriteErrorForRequest(w, r, http.StatusTooManyRequests, "global_concurrency_exceeded", "The gateway concurrency limit has been reached.")
				return
			}
			defer active.Add(-1)
			next.ServeHTTP(w, r)
		})
	}
}
