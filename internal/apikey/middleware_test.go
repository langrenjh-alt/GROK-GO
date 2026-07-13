package apikey

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

type fakeAuth struct{ key domain.ClientKey }

func (f fakeAuth) AuthenticateClientKey(context.Context, string) (*domain.ClientKey, error) {
	key := f.key
	return &key, nil
}

type fakeCounters struct {
	values   map[string]int64
	slots    int
	slotTTLs []time.Duration
}

func (f *fakeCounters) AddWindow(_ context.Context, key string, delta int64, _ time.Duration) (int64, error) {
	if f.values == nil {
		f.values = make(map[string]int64)
	}
	f.values[key] += delta
	return f.values[key], nil
}

func (f *fakeCounters) monthlyValue(keyID string) int64 {
	for key, value := range f.values {
		if strings.HasPrefix(key, "key:"+keyID+":tokens:month:") {
			return value
		}
	}
	return 0
}

func (f *fakeCounters) AcquireSlot(_ context.Context, _ string, limit int, ttl time.Duration) (bool, error) {
	if f.slots >= limit {
		return false, nil
	}
	f.slots++
	f.slotTTLs = append(f.slotTTLs, ttl)
	return true, nil
}

func TestMiddlewareSettlesParsedMonthlyUsage(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	counters := &fakeCounters{}
	middleware := Middleware{
		Auth:     fakeAuth{key: domain.ClientKey{ID: "key-usage", ConcurrencyLimit: 1, MonthlyTokenLimit: 100}},
		Counters: counters,
		Now:      func() time.Time { return now },
	}
	handler := middleware.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ReportCompletion(r.Context(), Completion{AccountID: "account-1", InputTokens: 7, OutputTokens: 3, CachedTokens: 2, UsageParsed: true})
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}
	if got := counters.monthlyValue("key-usage"); got != 10 {
		t.Fatalf("monthly tokens = %d, want 10", got)
	}
	if response.Header().Get("X-RateLimit-Remaining-Tokens") != "100" {
		t.Fatalf("remaining header = %q", response.Header().Get("X-RateLimit-Remaining-Tokens"))
	}
}

func TestMiddlewareDoesNotSettleUnparsedUsage(t *testing.T) {
	counters := &fakeCounters{}
	middleware := Middleware{
		Auth:     fakeAuth{key: domain.ClientKey{ID: "key-unparsed", ConcurrencyLimit: 1}},
		Counters: counters,
	}
	handler := middleware.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ReportCompletion(r.Context(), Completion{AccountID: "account-1"})
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if got := counters.monthlyValue("key-unparsed"); got != 0 {
		t.Fatalf("monthly tokens = %d, want 0", got)
	}
}

func TestMiddlewareAllowsUnlimitedKey(t *testing.T) {
	counters := &fakeCounters{}
	middleware := Middleware{
		Auth:     fakeAuth{key: domain.ClientKey{ID: "key-unlimited"}},
		Counters: counters,
	}
	called := false
	handler := middleware.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent || !called {
		t.Fatalf("status = %d, called = %v", response.Code, called)
	}
	if counters.slots != 0 {
		t.Fatalf("unlimited key acquired a concurrency slot: %d", counters.slots)
	}
}

func TestMiddlewareRejectsExhaustedMonthlyQuota(t *testing.T) {
	now := time.Date(2026, time.July, 14, 12, 0, 0, 0, time.UTC)
	monthKey, _ := monthlyTokenWindow("key-full", now)
	counters := &fakeCounters{values: map[string]int64{monthKey: 100}}
	middleware := Middleware{
		Auth:     fakeAuth{key: domain.ClientKey{ID: "key-full", ConcurrencyLimit: 1, MonthlyTokenLimit: 100}},
		Counters: counters,
		Now:      func() time.Time { return now },
	}
	called := false
	handler := middleware.Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusTooManyRequests || called {
		t.Fatalf("status = %d, called = %v", response.Code, called)
	}
	if !strings.Contains(response.Body.String(), "monthly_token_limit_exceeded") {
		t.Fatalf("unexpected response: %s", response.Body.String())
	}
}

func (f *fakeCounters) ReleaseSlot(context.Context, string) error {
	f.slots--
	return nil
}

func TestMiddlewareAuthenticatesAndAddsContext(t *testing.T) {
	counters := &fakeCounters{}
	middleware := Middleware{Auth: fakeAuth{key: domain.ClientKey{ID: "key-1", RPM: 60, ConcurrencyLimit: 1}}, Counters: counters}
	handler := middleware.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if key, ok := FromContext(r.Context()); !ok || key.ID != "key-1" {
			t.Fatal("client key missing from context")
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	request.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d", recorder.Code)
	}
	if counters.slots != 0 {
		t.Fatalf("slot leak: %d", counters.slots)
	}
}

func TestMiddlewareConcurrencySlotTTLTracksDynamicRequestTimeout(t *testing.T) {
	counters := &fakeCounters{}
	requestTimeout := 15 * time.Minute
	middleware := Middleware{
		Auth:               fakeAuth{key: domain.ClientKey{ID: "key-timeout", ConcurrencyLimit: 1}},
		Counters:           counters,
		RequestTimeoutFunc: func() time.Duration { return requestTimeout },
	}
	handler := middleware.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	serve := func() {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		request.Header.Set("Authorization", "Bearer secret")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNoContent {
			t.Fatalf("status = %d", response.Code)
		}
	}
	serve()
	requestTimeout = 90 * time.Minute
	serve()

	want := []time.Duration{65 * time.Minute, 95 * time.Minute}
	if len(counters.slotTTLs) != len(want) {
		t.Fatalf("slot TTL count = %d, want %d", len(counters.slotTTLs), len(want))
	}
	for index := range want {
		if counters.slotTTLs[index] != want[index] {
			t.Errorf("slot TTL %d = %s, want %s", index, counters.slotTTLs[index], want[index])
		}
	}
}

func TestMiddlewareDefaultConcurrencySlotTTLCoversMaximumRequest(t *testing.T) {
	middleware := Middleware{}
	if got := middleware.concurrencySlotTTL(); got < time.Hour {
		t.Fatalf("default slot TTL = %s, want at least %s", got, time.Hour)
	}
}

func TestMiddlewareUsesAnthropicErrorEnvelope(t *testing.T) {
	handler := Middleware{}.Handler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler should not be called")
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized || !strings.Contains(response.Body.String(), `"type":"error"`) || !strings.Contains(response.Body.String(), `"type":"authentication_error"`) {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
}
