package httpx

import (
	"context"
	"net/http"
	"sync"
	"time"
)

type Check struct {
	Name string
	Run  func(context.Context) error
}

func Liveness() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		WriteJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	})
}

func Readiness(timeout time.Duration, checks ...Check) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		results := make(map[string]string, len(checks))
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, check := range checks {
			check := check
			wg.Add(1)
			go func() {
				defer wg.Done()
				status := "ok"
				if err := check.Run(ctx); err != nil {
					status = "error"
				}
				mu.Lock()
				results[check.Name] = status
				mu.Unlock()
			}()
		}
		wg.Wait()

		status := http.StatusOK
		for _, result := range results {
			if result != "ok" {
				status = http.StatusServiceUnavailable
				break
			}
		}
		WriteJSON(w, status, map[string]any{
			"status": func() string {
				if status == http.StatusOK {
					return "ready"
				}
				return "not_ready"
			}(),
			"checks": results,
		})
	})
}
