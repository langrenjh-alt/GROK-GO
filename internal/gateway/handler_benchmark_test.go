package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/langrenjh-alt/GROK-GO/internal/accounts"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/upstream"
)

func BenchmarkChatCompletionsParallel(b *testing.B) {
	model := domain.ModelSpec{ID: "grok-benchmark", UpstreamModel: "grok-benchmark", Capability: domain.CapabilityChat, CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}, Enabled: true}
	account := domain.Account{ID: "benchmark", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, ConcurrencyLimit: 100_000, HealthScore: 1}
	store := accounts.NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{"benchmark": {AccessToken: "token"}})
	client := upstream.ClientFunc(func(context.Context, upstream.Request) (*upstream.Response, error) {
		return &upstream.Response{StatusCode: http.StatusOK, Events: upstream.Events(
			upstream.Event{Kind: upstream.EventTextDelta, Text: "ok"},
			upstream.Event{Kind: upstream.EventUsage, Usage: upstream.Usage{InputTokens: 2, OutputTokens: 1}},
			upstream.Event{Kind: upstream.EventDone},
		)}, nil
	})
	handler, err := NewHandler(Config{Models: NewStaticModelSource([]domain.ModelSpec{model}), Accounts: accounts.NewPool(store, accounts.DefaultPolicy()), Upstream: client})
	if err != nil {
		b.Fatal(err)
	}
	body := `{"model":"grok-benchmark","messages":[{"role":"user","content":"hello"}]}`
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				b.Fatalf("status = %d", response.Code)
			}
		}
	})
}
