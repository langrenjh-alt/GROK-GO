package requestlog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/apikey"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/httpx"
)

type testAuth struct{}

func (testAuth) AuthenticateClientKey(context.Context, string) (*domain.ClientKey, error) {
	return &domain.ClientKey{ID: "key-1", ConcurrencyLimit: 1, MonthlyTokenLimit: 100}, nil
}

type testCounters struct {
	values map[string]int64
}

func (c *testCounters) AddWindow(_ context.Context, key string, delta int64, _ time.Duration) (int64, error) {
	if c.values == nil {
		c.values = make(map[string]int64)
	}
	c.values[key] += delta
	return c.values[key], nil
}

func (*testCounters) AcquireSlot(context.Context, string, int, time.Duration) (bool, error) {
	return true, nil
}

func (*testCounters) ReleaseSlot(context.Context, string) error { return nil }

type testSink struct {
	entry *domain.RequestLog
}

func (s *testSink) CreateRequestLog(_ context.Context, entry *domain.RequestLog) error {
	copy := *entry
	s.entry = &copy
	return nil
}

func TestMiddlewareRecordsCompletionUsage(t *testing.T) {
	sink := &testSink{}
	inner := Middleware{Sink: sink}.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apikey.ReportCompletion(r.Context(), apikey.Completion{
			AccountID: "account-1", InputTokens: 9, OutputTokens: 4, CachedTokens: 3, UsageParsed: true,
		})
		w.WriteHeader(http.StatusOK)
	}))
	handler := apikey.Middleware{Auth: testAuth{}, Counters: &testCounters{}}.Handler(inner)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"grok-chat"}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || sink.entry == nil {
		t.Fatalf("status = %d, log = %+v", response.Code, sink.entry)
	}
	if sink.entry.ClientKeyID != "key-1" || sink.entry.AccountID != "account-1" || sink.entry.Model != "grok-chat" {
		t.Fatalf("unexpected log identity: %+v", sink.entry)
	}
	if sink.entry.InputTokens != 9 || sink.entry.OutputTokens != 4 || sink.entry.CachedTokens != 3 || !sink.entry.UsageParsed {
		t.Fatalf("unexpected log usage: %+v", sink.entry)
	}
}

func TestMiddlewareRecordsOnlyRedactedHTTPErrorMetadata(t *testing.T) {
	sink := &testSink{}
	inner := Middleware{Sink: sink}.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "sensitive upstream detail", http.StatusBadGateway)
	}))
	handler := apikey.Middleware{Auth: testAuth{}, Counters: &testCounters{}}.Handler(inner)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"grok-chat"}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(httptest.NewRecorder(), request)
	if sink.entry == nil || sink.entry.ErrorCode != "http_502" || sink.entry.ErrorSummary != "Bad Gateway" {
		t.Fatalf("unexpected error metadata: %+v", sink.entry)
	}
}

func TestMiddlewareRecordsStreamOutcomeAfterHTTP200(t *testing.T) {
	sink := &testSink{}
	handler := Middleware{Sink: sink}.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		httpx.ReportOutcome(r.Context(), http.StatusBadGateway, "upstream_stream_error")
	}))
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"grok-chat","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	if sink.entry == nil || sink.entry.StatusCode != http.StatusBadGateway || sink.entry.ErrorCode != "upstream_stream_error" || sink.entry.ErrorSummary != "Bad Gateway" {
		t.Fatalf("unexpected stream log: %+v", sink.entry)
	}
}
