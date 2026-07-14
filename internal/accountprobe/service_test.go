package accountprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/accounts"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/upstream"
)

func TestProbeUsesSelectedCredentialsProxyAndPersistsQuota(t *testing.T) {
	account := domain.Account{ID: "account-1", Kind: domain.CredentialGrokSSO, Status: domain.AccountActive, ProxyID: "proxy-1", Models: []string{"local-model-alias"}, ConcurrencyLimit: 1, HealthScore: 1}
	memory := accounts.NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{account.ID: {SSO: "secret"}})
	pool := accounts.NewPool(memory, accounts.DefaultPolicy())
	client := upstream.ClientFunc(func(_ context.Context, request upstream.Request) (*upstream.Response, error) {
		if request.Credentials.SSO != "secret" || request.ProxyURL != "socks5://proxy.test:1080" || request.Model != "grok-4.20-fast" {
			t.Fatalf("request = %+v", request)
		}
		var payload map[string]any
		if err := json.Unmarshal(request.Body, &payload); err != nil || payload["input"] != "Reply with OK." {
			t.Fatalf("payload = %s, err = %v", request.Body, err)
		}
		header := http.Header{}
		header.Set("X-RateLimit-Limit-Requests", "100")
		header.Set("X-RateLimit-Remaining-Requests", "79")
		return &upstream.Response{StatusCode: http.StatusOK, Header: header, Events: upstream.Events(upstream.Event{Kind: upstream.EventTextDelta, Text: "OK"}, upstream.Event{Kind: upstream.EventDone})}, nil
	})
	service, err := New(Config{Accounts: pool, Reader: memory, Upstream: client, ProxyURL: func(_ context.Context, id string) (string, error) {
		if id != "proxy-1" {
			t.Fatalf("proxy id = %s", id)
		}
		return "socks5://proxy.test:1080", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Probe(context.Background(), account.ID, Input{})
	if err != nil || !result.Success || result.StatusCode != http.StatusOK || result.Account == nil {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if result.Account.Quota.RequestsLimit != 100 || result.Account.Quota.RequestsRemaining != 79 {
		t.Fatalf("quota not persisted: %+v", result.Account.Quota)
	}
}

func TestProbeTurnsCredentialRejectionIntoDisabledAccount(t *testing.T) {
	account := domain.Account{ID: "rejected", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, ConcurrencyLimit: 1, HealthScore: 1}
	memory := accounts.NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{account.ID: {AccessToken: "expired"}})
	pool := accounts.NewPool(memory, accounts.DefaultPolicy())
	client := upstream.ClientFunc(func(context.Context, upstream.Request) (*upstream.Response, error) {
		return &upstream.Response{StatusCode: http.StatusForbidden, Header: http.Header{}, Body: json.RawMessage(`{"error":{"message":"account blocked"}}`)}, nil
	})
	service, err := New(Config{Accounts: pool, Reader: memory, Upstream: client})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Probe(context.Background(), account.ID, Input{})
	if err != nil || result.Success || result.Account == nil {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if result.Account.Status != domain.AccountDisabled || result.Account.FailureCount != 1 || result.Account.HealthScore >= 1 || result.Account.LastError == "" {
		t.Fatalf("rejection state = %+v", result.Account)
	}
}

func TestProbeTreatsCloudflareChallengeAsTransientAndRedactsHTML(t *testing.T) {
	account := domain.Account{ID: "challenged", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, ConcurrencyLimit: 1, HealthScore: 1}
	memory := accounts.NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{account.ID: {AccessToken: "valid-token"}})
	pool := accounts.NewPool(memory, accounts.DefaultPolicy())
	challenge := `<!DOCTYPE html><html><head><title>Just a moment...</title></head><body><script src="https://challenges.cloudflare.com/turnstile/v0/api.js">credential-shaped-secret</script></body></html>`
	client := upstream.ClientFunc(func(context.Context, upstream.Request) (*upstream.Response, error) {
		return &upstream.Response{
			StatusCode: http.StatusForbidden,
			Header:     http.Header{"Content-Type": {"text/html; charset=UTF-8"}, "Server": {"cloudflare"}},
			Body:       json.RawMessage(challenge),
		}, nil
	})
	service, err := New(Config{Accounts: pool, Reader: memory, Upstream: client})
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.Probe(context.Background(), account.ID, Input{})
	if err != nil || result.Success || result.Account == nil {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if result.StatusCode != http.StatusForbidden || result.Account.Status != domain.AccountCooldown || result.Account.CooldownUntil == nil {
		t.Fatalf("challenge state = %+v", result)
	}
	if result.Account.Status == domain.AccountDisabled {
		t.Fatalf("challenge permanently disabled account: %+v", result.Account)
	}
	for _, value := range []string{result.Message, result.Account.LastError} {
		lower := strings.ToLower(value)
		if strings.Contains(lower, "<!doctype") || strings.Contains(lower, "<html") || strings.Contains(lower, "credential-shaped-secret") {
			t.Fatalf("HTML challenge leaked into persisted/displayed error: %q", value)
		}
	}
	if result.Message != "upstream protection challenge returned HTTP 403; check the account proxy and retry" {
		t.Fatalf("challenge message = %q", result.Message)
	}
}

func TestUpstreamResponseErrorRedactsGenericHTML(t *testing.T) {
	response := &upstream.Response{
		StatusCode: http.StatusBadGateway,
		Header:     http.Header{"Content-Type": {"text/html"}},
		Body:       json.RawMessage(`<!doctype html><html><body>internal-edge-secret</body></html>`),
	}
	message := upstreamResponseError(response).Error()
	if message != "upstream returned HTTP 502: Bad Gateway" {
		t.Fatalf("sanitized message = %q", message)
	}
}

func TestUpstreamResponseErrorRedactsJSONDetail(t *testing.T) {
	response := &upstream.Response{
		StatusCode: http.StatusUnauthorized,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       json.RawMessage(`{"error":{"message":"echoed-access-token-secret"}}`),
	}
	message := upstreamResponseError(response).Error()
	if message != "upstream returned HTTP 401: Unauthorized" || strings.Contains(message, "echoed-access-token-secret") {
		t.Fatalf("sanitized message = %q", message)
	}
}

func TestUpstreamResponseErrorRecognizesCFMitigatedChallenge(t *testing.T) {
	header := http.Header{"Content-Type": {"text/html"}}
	header.Set("CF-Mitigated", "challenge")
	response := &upstream.Response{
		StatusCode: http.StatusForbidden,
		Header:     header,
		Body:       json.RawMessage(`<!doctype html><html><body>edge verification</body></html>`),
	}
	var challenge *upstreamProtectionChallengeError
	if err := upstreamResponseError(response); !errors.As(err, &challenge) {
		t.Fatalf("error = %v, want protection challenge classification", err)
	}
}

func TestProbeUsesCurrentCLIModel(t *testing.T) {
	account := domain.Account{ID: "cli", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, ConcurrencyLimit: 1, HealthScore: 1}
	memory := accounts.NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{account.ID: {AccessToken: "token"}})
	pool := accounts.NewPool(memory, accounts.DefaultPolicy())
	client := upstream.ClientFunc(func(_ context.Context, request upstream.Request) (*upstream.Response, error) {
		if request.Model != "grok-4.5" || request.UpstreamModel != "grok-4.5" {
			t.Fatalf("CLI probe model = %q/%q", request.Model, request.UpstreamModel)
		}
		return &upstream.Response{StatusCode: http.StatusOK, Events: upstream.Events(upstream.Event{Kind: upstream.EventDone})}, nil
	})
	service, err := New(Config{Accounts: pool, Reader: memory, Upstream: client})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Probe(context.Background(), account.ID, Input{})
	if err != nil || !result.Success || result.Model != "grok-4.5" {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestProbeRateLimitAppliesCooldown(t *testing.T) {
	account := domain.Account{ID: "limited", Kind: domain.CredentialGrokSSO, Status: domain.AccountActive, ConcurrencyLimit: 1, HealthScore: 1}
	memory := accounts.NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{account.ID: {SSO: "limited"}})
	pool := accounts.NewPool(memory, accounts.DefaultPolicy())
	client := upstream.ClientFunc(func(context.Context, upstream.Request) (*upstream.Response, error) {
		return &upstream.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"60"}}, Body: json.RawMessage(`{"error":"rate limited"}`)}, nil
	})
	service, err := New(Config{Accounts: pool, Reader: memory, Upstream: client})
	if err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	result, err := service.Probe(context.Background(), account.ID, Input{})
	if err != nil || result.Success || result.Account == nil {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
	if result.Account.Status != domain.AccountCooldown || result.Account.CooldownUntil == nil || result.Account.CooldownUntil.Before(before.Add(55*time.Second)) || result.Account.CooldownUntil.After(before.Add(65*time.Second)) {
		t.Fatalf("rate-limit state = %+v", result.Account)
	}
}

func TestProbeDoesNotTreatEmptyTwoHundredResponseAsSuccess(t *testing.T) {
	account := domain.Account{ID: "empty", Kind: domain.CredentialGrokSSO, Status: domain.AccountActive, ConcurrencyLimit: 1, HealthScore: 1}
	memory := accounts.NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{account.ID: {SSO: "credential"}})
	pool := accounts.NewPool(memory, accounts.DefaultPolicy())
	client := upstream.ClientFunc(func(context.Context, upstream.Request) (*upstream.Response, error) {
		return &upstream.Response{StatusCode: http.StatusOK, Events: upstream.Events()}, nil
	})
	service, err := New(Config{Accounts: pool, Reader: memory, Upstream: client})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Probe(context.Background(), account.ID, Input{})
	if err != nil || result.Success || result.Account == nil || result.Account.Status != domain.AccountCooldown {
		t.Fatalf("empty response result = %+v, err = %v", result, err)
	}
	if result.Message != "upstream returned no parseable response events" {
		t.Fatalf("empty response message = %q", result.Message)
	}
}

func TestProbeRedactsUpstreamErrorEvent(t *testing.T) {
	account := domain.Account{ID: "error-event", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, ConcurrencyLimit: 1, HealthScore: 1}
	memory := accounts.NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{account.ID: {AccessToken: "token"}})
	pool := accounts.NewPool(memory, accounts.DefaultPolicy())
	client := upstream.ClientFunc(func(context.Context, upstream.Request) (*upstream.Response, error) {
		return &upstream.Response{StatusCode: http.StatusOK, Events: upstream.Events(upstream.Event{Kind: upstream.EventError, Error: "echoed-sensitive-value"})}, nil
	})
	service, err := New(Config{Accounts: pool, Reader: memory, Upstream: client})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Probe(context.Background(), account.ID, Input{})
	if err != nil || result.Success || result.Message != "upstream returned an error event" || strings.Contains(result.Message, "echoed-sensitive-value") {
		t.Fatalf("error event result = %+v, err = %v", result, err)
	}
}

func TestProbeManyUsesBoundedParallelism(t *testing.T) {
	const count = 12
	items := make([]domain.Account, 0, count)
	credentials := make(map[string]domain.Credentials, count)
	ids := make([]string, 0, count)
	for index := range count {
		id := fmt.Sprintf("account-%d", index)
		items = append(items, domain.Account{ID: id, Kind: domain.CredentialGrokSSO, Status: domain.AccountActive, ConcurrencyLimit: 1, HealthScore: 1})
		credentials[id] = domain.Credentials{SSO: id}
		ids = append(ids, id)
	}
	memory := accounts.NewMemoryStore(items, credentials)
	pool := accounts.NewPool(memory, accounts.DefaultPolicy())
	var active, maximum atomic.Int32
	client := upstream.ClientFunc(func(ctx context.Context, _ upstream.Request) (*upstream.Response, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return &upstream.Response{StatusCode: http.StatusOK, Events: upstream.Events(upstream.Event{Kind: upstream.EventDone})}, nil
	})
	service, err := New(Config{Accounts: pool, Reader: memory, Upstream: client, Parallelism: 3})
	if err != nil {
		t.Fatal(err)
	}
	result := service.ProbeMany(context.Background(), ids, Input{})
	if result.Succeeded != count || result.Failed != 0 || maximum.Load() > 3 {
		t.Fatalf("batch = %+v, maximum concurrency = %d", result, maximum.Load())
	}
}
