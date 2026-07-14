package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/apikey"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/upstream"
)

type cacheIdentityAuth struct{}

func (cacheIdentityAuth) AuthenticateClientKey(_ context.Context, token string) (*domain.ClientKey, error) {
	return &domain.ClientKey{ID: "authenticated-" + token, Enabled: true}, nil
}

type cacheIdentityCounters struct{}

func (cacheIdentityCounters) AddWindow(context.Context, string, int64, time.Duration) (int64, error) {
	return 0, nil
}

func (cacheIdentityCounters) AcquireSlot(context.Context, string, int, time.Duration) (bool, error) {
	return true, nil
}

func (cacheIdentityCounters) ReleaseSlot(context.Context, string) error { return nil }

func TestGatewayPromptCacheIdentityIsGlobalAcrossAuthenticatedKeys(t *testing.T) {
	var mu sync.Mutex
	keys := make([]string, 0, 2)
	client := upstream.ClientFunc(func(_ context.Context, request upstream.Request) (*upstream.Response, error) {
		mu.Lock()
		keys = append(keys, request.PromptCacheKey)
		mu.Unlock()
		return &upstream.Response{StatusCode: http.StatusOK, Events: upstream.Events(
			upstream.Event{Kind: upstream.EventTextDelta, Text: "ok"},
			upstream.Event{Kind: upstream.EventDone},
		)}, nil
	})
	handler := apikey.Middleware{Auth: cacheIdentityAuth{}, Counters: cacheIdentityCounters{}}.Handler(testGateway(t, client))

	requests := []*http.Request{
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"grok-chat","messages":[{"role":"system","content":"Shared system"},{"role":"user","content":"First"}]}`)),
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"grok-chat","messages":[{"role":"developer","content":"Shared system"},{"role":"user","content":"Second"}]}`)),
	}
	requests[0].Header.Set("Authorization", "Bearer bearer-secret")
	requests[0].Header.Set("Session-Id", "session-a")
	requests[1].Header.Set("X-API-Key", "x-api-key-secret")
	requests[1].Header.Set("Session-Id", "session-b")
	for _, request := range requests {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("gateway response = %d %s", response.Code, response.Body.String())
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("prompt cache keys = %v, want the same non-empty static-prefix key", keys)
	}
}
