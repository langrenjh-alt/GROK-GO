package apikey

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/admin"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/httpx"
)

type contextKey string

const clientKeyContextKey contextKey = "client_key"

const completionContextKey contextKey = "completion"

const (
	defaultMaximumRequestDuration = time.Hour
	concurrencySlotTTLGrace       = 5 * time.Minute
)

type CounterStore interface {
	AddWindow(context.Context, string, int64, time.Duration) (int64, error)
	AcquireSlot(context.Context, string, int, time.Duration) (bool, error)
	ReleaseSlot(context.Context, string) error
}

type Authenticator interface {
	AuthenticateClientKey(context.Context, string) (*domain.ClientKey, error)
}

type Middleware struct {
	Auth               Authenticator
	Counters           CounterStore
	Now                func() time.Time
	RequestTimeoutFunc func() time.Duration
}

// Completion contains the upstream account and token usage observed while a
// request was being served. UsageParsed distinguishes a real zero-token usage
// report from a response that did not include usage at all.
type Completion struct {
	AccountID                string
	InputTokens              int64
	OutputTokens             int64
	CachedTokens             int64
	UsageParsed              bool
	CacheIdentityApplied     bool
	CacheAffinityReused      bool
	CacheAffinityEstablished bool
}

type completionTracker struct {
	mu         sync.RWMutex
	completion Completion
}

func (t *completionTracker) record(value Completion) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if value.AccountID != "" {
		t.completion.AccountID = value.AccountID
	}
	if value.CacheIdentityApplied {
		t.completion.CacheIdentityApplied = true
		t.completion.CacheAffinityReused = value.CacheAffinityReused
		t.completion.CacheAffinityEstablished = value.CacheAffinityEstablished
	}
	if value.UsageParsed {
		value.InputTokens = max(0, value.InputTokens)
		value.OutputTokens = max(0, value.OutputTokens)
		value.CachedTokens = max(0, value.CachedTokens)
		value.AccountID = t.completion.AccountID
		value.CacheIdentityApplied = t.completion.CacheIdentityApplied
		value.CacheAffinityReused = t.completion.CacheAffinityReused
		value.CacheAffinityEstablished = t.completion.CacheAffinityEstablished
		t.completion = value
	}
}

func (t *completionTracker) snapshot() Completion {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.completion
}

func (m Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plaintext := credential(r)
		if plaintext == "" {
			httpx.WriteErrorForRequest(w, r, http.StatusUnauthorized, "invalid_api_key", "A valid API key is required.")
			return
		}
		key, err := m.Auth.AuthenticateClientKey(r.Context(), plaintext)
		if err != nil {
			status := http.StatusUnauthorized
			if !errors.Is(err, admin.ErrInvalidCredentials) {
				status = http.StatusServiceUnavailable
			}
			httpx.WriteErrorForRequest(w, r, status, "invalid_api_key", "The API key is invalid or inactive.")
			return
		}
		if m.Counters == nil {
			httpx.WriteErrorForRequest(w, r, http.StatusServiceUnavailable, "rate_limit_unavailable", "Rate limiting is unavailable.")
			return
		}
		now := time.Now().UTC()
		if m.Now != nil {
			now = m.Now().UTC()
		}
		if key.RPM > 0 {
			window := now.Truncate(time.Minute)
			count, countErr := m.Counters.AddWindow(r.Context(), fmt.Sprintf("key:%s:rpm:%d", key.ID, window.Unix()), 1, 2*time.Minute)
			if countErr != nil {
				httpx.WriteErrorForRequest(w, r, http.StatusServiceUnavailable, "rate_limit_unavailable", "Rate limiting is unavailable.")
				return
			}
			w.Header().Set("X-RateLimit-Limit-Requests", fmt.Sprint(key.RPM))
			w.Header().Set("X-RateLimit-Remaining-Requests", fmt.Sprint(max(0, int64(key.RPM)-count)))
			if count > int64(key.RPM) {
				w.Header().Set("Retry-After", fmt.Sprint(60-now.Second()))
				httpx.WriteErrorForRequest(w, r, http.StatusTooManyRequests, "rate_limit_exceeded", "The API key request rate was exceeded.")
				return
			}
		}
		if key.DailyRequestLimit > 0 {
			day := now.Truncate(24 * time.Hour)
			count, countErr := m.Counters.AddWindow(r.Context(), fmt.Sprintf("key:%s:day:%d", key.ID, day.Unix()), 1, 26*time.Hour)
			if countErr != nil {
				httpx.WriteErrorForRequest(w, r, http.StatusServiceUnavailable, "rate_limit_unavailable", "Rate limiting is unavailable.")
				return
			}
			if count > key.DailyRequestLimit {
				httpx.WriteErrorForRequest(w, r, http.StatusTooManyRequests, "daily_limit_exceeded", "The API key daily request limit was exceeded.")
				return
			}
		}
		monthKey, monthTTL := monthlyTokenWindow(key.ID, now)
		if key.MonthlyTokenLimit > 0 {
			used, countErr := m.Counters.AddWindow(r.Context(), monthKey, 0, monthTTL)
			if countErr != nil {
				httpx.WriteErrorForRequest(w, r, http.StatusServiceUnavailable, "rate_limit_unavailable", "Rate limiting is unavailable.")
				return
			}
			w.Header().Set("X-RateLimit-Limit-Tokens", fmt.Sprint(key.MonthlyTokenLimit))
			w.Header().Set("X-RateLimit-Remaining-Tokens", fmt.Sprint(max(0, key.MonthlyTokenLimit-used)))
			if used >= key.MonthlyTokenLimit {
				httpx.WriteErrorForRequest(w, r, http.StatusTooManyRequests, "monthly_token_limit_exceeded", "The API key monthly token limit was exceeded.")
				return
			}
		}
		if key.ConcurrencyLimit > 0 {
			slotKey := "key:" + key.ID + ":concurrency"
			acquired, err := m.Counters.AcquireSlot(r.Context(), slotKey, key.ConcurrencyLimit, m.concurrencySlotTTL())
			if err != nil {
				httpx.WriteErrorForRequest(w, r, http.StatusServiceUnavailable, "rate_limit_unavailable", "Concurrency limiting is unavailable.")
				return
			}
			if !acquired {
				httpx.WriteErrorForRequest(w, r, http.StatusTooManyRequests, "concurrency_limit_exceeded", "The API key concurrency limit was exceeded.")
				return
			}
			defer func() { _ = m.Counters.ReleaseSlot(context.Background(), slotKey) }()
		}
		tracker := &completionTracker{}
		requestContext := context.WithValue(r.Context(), clientKeyContextKey, *key)
		requestContext = context.WithValue(requestContext, completionContextKey, tracker)
		next.ServeHTTP(w, r.WithContext(requestContext))

		completion := tracker.snapshot()
		if completion.UsageParsed {
			settlementContext, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _ = m.Counters.AddWindow(settlementContext, monthKey, completion.InputTokens+completion.OutputTokens, monthTTL)
		}
	})
}

func (m Middleware) concurrencySlotTTL() time.Duration {
	// Start from the maximum accepted runtime request timeout so a live setting
	// increase between slot acquisition and the upstream call cannot shorten the lease.
	requestDuration := defaultMaximumRequestDuration
	if m.RequestTimeoutFunc != nil {
		if configured := m.RequestTimeoutFunc(); configured > requestDuration {
			requestDuration = configured
		}
	}
	if requestDuration > time.Duration(1<<63-1)-concurrencySlotTTLGrace {
		return requestDuration
	}
	return requestDuration + concurrencySlotTTLGrace
}

func FromContext(ctx context.Context) (domain.ClientKey, bool) {
	key, ok := ctx.Value(clientKeyContextKey).(domain.ClientKey)
	return key, ok
}

// ReportCompletion records the final upstream usage for the current API
// request. It is intentionally in-memory so streaming handlers can report
// after their final parsed usage event without performing I/O on the stream.
func ReportCompletion(ctx context.Context, value Completion) {
	tracker, _ := ctx.Value(completionContextKey).(*completionTracker)
	if tracker != nil {
		tracker.record(value)
	}
}

// CompletionFromContext returns the latest completion data for request
// logging and other post-response middleware.
func CompletionFromContext(ctx context.Context) (Completion, bool) {
	tracker, _ := ctx.Value(completionContextKey).(*completionTracker)
	if tracker == nil {
		return Completion{}, false
	}
	return tracker.snapshot(), true
}

func monthlyTokenWindow(keyID string, now time.Time) (string, time.Duration) {
	now = now.UTC()
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	next := start.AddDate(0, 1, 0)
	return fmt.Sprintf("key:%s:tokens:month:%d", keyID, start.Unix()), next.Sub(now) + time.Hour
}

func credential(r *http.Request) string {
	if value := strings.TrimSpace(r.Header.Get("X-API-Key")); value != "" {
		return value
	}
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(value) > 7 && strings.EqualFold(value[:7], "Bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}
