package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/accounts"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/httpx"
	"github.com/langrenjh-alt/GROK-GO/internal/upstream"
)

func testGateway(t *testing.T, client upstream.Client) http.Handler {
	return testGatewayWithCompletion(t, client, nil)
}

func testGatewayWithCompletion(t *testing.T, client upstream.Client, onCompletion func(context.Context, Completion)) http.Handler {
	t.Helper()
	models := NewStaticModelSource([]domain.ModelSpec{
		{ID: "grok-chat", UpstreamModel: "grok-upstream", DisplayName: "Grok", Capability: domain.CapabilityChat, CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}, Enabled: true},
		{ID: "grok-image", UpstreamModel: "grok-image", DisplayName: "Grok Image", Capability: domain.CapabilityImage, CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}, Enabled: true},
		{ID: "grok-image-edit", UpstreamModel: "grok-image-edit", DisplayName: "Grok Image Edit", Capability: domain.CapabilityImageEdit, CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}, Enabled: true},
		{ID: "grok-video", UpstreamModel: "grok-video", DisplayName: "Grok Video", Capability: domain.CapabilityVideo, CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}, Enabled: true},
	})
	store := accounts.NewMemoryStore([]domain.Account{{ID: "account", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, ConcurrencyLimit: 1}}, map[string]domain.Credentials{"account": {AccessToken: "access"}})
	pool := accounts.NewPool(store, accounts.DefaultPolicy())
	handler, err := NewHandler(Config{Models: models, Accounts: pool, Upstream: client, OnCompletion: onCompletion, HeartbeatInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestCompletionReportsParsedUsageAndAccount(t *testing.T) {
	client := upstream.ClientFunc(func(_ context.Context, _ upstream.Request) (*upstream.Response, error) {
		return &upstream.Response{StatusCode: http.StatusOK, Events: upstream.Events(
			upstream.Event{Kind: upstream.EventTextDelta, Text: "answer"},
			upstream.Event{Kind: upstream.EventUsage, Usage: upstream.Usage{InputTokens: 5, OutputTokens: 3, CachedTokens: 2}},
			upstream.Event{Kind: upstream.EventDone},
		)}, nil
	})
	var completions []Completion
	handler := testGatewayWithCompletion(t, client, func(_ context.Context, completion Completion) {
		completions = append(completions, completion)
	})

	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"grok-chat","messages":[{"role":"user","content":"hi"}]}`))
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if len(completions) != 1 {
		t.Fatalf("completion count = %d", len(completions))
	}
	completion := completions[0]
	if completion.AccountID != "account" || !completion.UsageParsed || completion.Usage.InputTokens != 5 || completion.Usage.OutputTokens != 3 || completion.Usage.CachedTokens != 2 {
		t.Fatalf("unexpected completion: %+v", completion)
	}
}

func TestStreamingCompletionMarksMissingUsageUnparsed(t *testing.T) {
	client := upstream.ClientFunc(func(_ context.Context, _ upstream.Request) (*upstream.Response, error) {
		return &upstream.Response{StatusCode: http.StatusOK, Events: upstream.Events(
			upstream.Event{Kind: upstream.EventTextDelta, Text: "answer"},
			upstream.Event{Kind: upstream.EventDone},
		)}, nil
	})
	var completion Completion
	handler := testGatewayWithCompletion(t, client, func(_ context.Context, value Completion) { completion = value })
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"grok-chat","stream":true,"messages":[]}`))
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || completion.AccountID != "account" || completion.UsageParsed {
		t.Fatalf("status = %d, completion = %+v", response.Code, completion)
	}
}

func TestAnthropicErrorsUseMessagesEnvelope(t *testing.T) {
	client := upstream.ClientFunc(func(_ context.Context, _ upstream.Request) (*upstream.Response, error) {
		return &upstream.Response{StatusCode: http.StatusUnauthorized, Body: json.RawMessage(`{"error":{"message":"expired"}}`)}, nil
	})
	handler := testGateway(t, client)

	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"messages":[]}`)))
	if missing.Code != http.StatusBadRequest || !strings.Contains(missing.Body.String(), `"type":"error"`) || !strings.Contains(missing.Body.String(), `"type":"invalid_request_error"`) {
		t.Fatalf("missing model response = %d %s", missing.Code, missing.Body.String())
	}

	upstreamError := httptest.NewRecorder()
	handler.ServeHTTP(upstreamError, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"grok-chat","messages":[]}`)))
	if upstreamError.Code != http.StatusBadGateway || !strings.Contains(upstreamError.Body.String(), `"type":"api_error"`) || strings.Contains(upstreamError.Body.String(), `"code":"upstream_error"`) {
		t.Fatalf("upstream response = %d %s", upstreamError.Code, upstreamError.Body.String())
	}
}

func TestStreamingClearsServerWriteDeadline(t *testing.T) {
	client := upstream.ClientFunc(func(_ context.Context, _ upstream.Request) (*upstream.Response, error) {
		return &upstream.Response{StatusCode: http.StatusOK, Events: upstream.Events(upstream.Event{Kind: upstream.EventDone})}, nil
	})
	handler := testGateway(t, client)
	response := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"grok-chat","stream":true,"messages":[]}`))
	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK || len(response.deadlines) == 0 || !response.deadlines[len(response.deadlines)-1].IsZero() {
		t.Fatalf("status = %d, deadlines = %v", response.Code, response.deadlines)
	}
}

func TestStreamingReportsPostCommitFailureOutcome(t *testing.T) {
	client := upstream.ClientFunc(func(_ context.Context, _ upstream.Request) (*upstream.Response, error) {
		return &upstream.Response{StatusCode: http.StatusOK, Events: upstream.Events(upstream.Event{Kind: upstream.EventError, Error: "stream failed"})}, nil
	})
	handler := testGateway(t, client)
	ctx, tracker := httpx.WithOutcome(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"grok-chat","stream":true,"messages":[]}`)).WithContext(ctx)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)

	outcome := tracker.Snapshot()
	if response.Code != http.StatusOK || outcome.StatusCode != http.StatusBadGateway || outcome.ErrorCode != "upstream_stream_error" {
		t.Fatalf("response = %d, outcome = %+v, body = %s", response.Code, outcome, response.Body.String())
	}
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (w *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}

func TestModelDetailAndVideoAliases(t *testing.T) {
	videoStore := NewMemoryVideoStore()
	models := NewStaticModelSource([]domain.ModelSpec{{ID: "grok-video", UpstreamModel: "grok-video", DisplayName: "Grok Video", Capability: domain.CapabilityVideo, CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}, Enabled: true}})
	store := accounts.NewMemoryStore([]domain.Account{{ID: "account", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive}}, map[string]domain.Credentials{"account": {AccessToken: "access"}})
	client := upstream.ClientFunc(func(_ context.Context, request upstream.Request) (*upstream.Response, error) {
		if request.Operation != upstream.OperationVideo {
			t.Fatalf("unexpected operation: %s", request.Operation)
		}
		return &upstream.Response{StatusCode: http.StatusOK, Body: json.RawMessage(`{"id":"video_1","object":"video","status":"queued"}`)}, nil
	})
	handler, err := NewHandler(Config{Models: models, Accounts: accounts.NewPool(store, accounts.DefaultPolicy()), Upstream: client, Videos: videoStore})
	if err != nil {
		t.Fatal(err)
	}

	detail := httptest.NewRecorder()
	handler.ServeHTTP(detail, httptest.NewRequest(http.MethodGet, "/v1/models/grok-video", nil))
	if detail.Code != http.StatusOK || !strings.Contains(detail.Body.String(), `"id":"grok-video"`) {
		t.Fatalf("unexpected model detail: %d %s", detail.Code, detail.Body.String())
	}

	created := httptest.NewRecorder()
	handler.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/v1/videos/generations", strings.NewReader(`{"model":"grok-video","prompt":"x"}`)))
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), "video_1") {
		t.Fatalf("unexpected video create response: %d %s", created.Code, created.Body.String())
	}

	retrieved := httptest.NewRecorder()
	handler.ServeHTTP(retrieved, httptest.NewRequest(http.MethodGet, "/v1/videos/video_1", nil))
	if retrieved.Code != http.StatusOK || !strings.Contains(retrieved.Body.String(), `"status":"queued"`) {
		t.Fatalf("unexpected video retrieve response: %d %s", retrieved.Code, retrieved.Body.String())
	}

	videoStore.SetVideoContent("video_1", domain.MediaObject{ID: "video_1", ContentType: "video/mp4", Size: 3}, []byte("mp4"))
	content := httptest.NewRecorder()
	handler.ServeHTTP(content, httptest.NewRequest(http.MethodGet, "/v1/videos/video_1/content", nil))
	if content.Code != http.StatusOK || content.Body.String() != "mp4" || content.Header().Get("Content-Type") != "video/mp4" {
		t.Fatalf("unexpected video content response: %d %s", content.Code, content.Body.String())
	}
}

func TestChatStreamingEmitsHeartbeatChunksAndReleasesLease(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	client := upstream.ClientFunc(func(_ context.Context, request upstream.Request) (*upstream.Response, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		if request.Credentials.AccessToken != "access" || request.UpstreamModel != "grok-upstream" {
			t.Fatalf("unexpected upstream request: %#v", request)
		}
		return &upstream.Response{StatusCode: http.StatusOK, Header: make(http.Header), Events: upstream.Events(
			upstream.Event{Kind: upstream.EventTextDelta, Text: "hello"},
			upstream.Event{Kind: upstream.EventUsage, Usage: upstream.Usage{InputTokens: 2, OutputTokens: 1}},
			upstream.Event{Kind: upstream.EventDone},
		)}, nil
	})
	handler := testGateway(t, client)
	for range 2 {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"grok-chat","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), ": heartbeat") || !strings.Contains(response.Body.String(), `"content":"hello"`) || !strings.Contains(response.Body.String(), "data: [DONE]") {
			t.Fatalf("unexpected stream response: status=%d body=%s", response.Code, response.Body.String())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("expected two calls after lease release, got %d", calls)
	}
}

func TestStreamingToolCallsPreserveProtocolLifecycle(t *testing.T) {
	client := upstream.ClientFunc(func(_ context.Context, _ upstream.Request) (*upstream.Response, error) {
		return &upstream.Response{StatusCode: http.StatusOK, Events: upstream.Events(
			upstream.Event{Kind: upstream.EventToolCall, ID: "call_1", ItemID: "item_1", Name: "lookup", Index: 0},
			upstream.Event{Kind: upstream.EventToolCall, ID: "call_1", ItemID: "item_1", Name: "lookup", Arguments: `{"q":`, Index: 0},
			upstream.Event{Kind: upstream.EventToolCall, ID: "call_1", ItemID: "item_1", Name: "lookup", Arguments: `"x"}`, Index: 0},
			upstream.Event{Kind: upstream.EventUsage, Usage: upstream.Usage{InputTokens: 3, OutputTokens: 2}},
			upstream.Event{Kind: upstream.EventDone},
		)}, nil
	})
	handler := testGateway(t, client)
	tests := []struct {
		path  string
		body  string
		wants []string
	}{
		{path: "/v1/chat/completions", body: `{"model":"grok-chat","stream":true,"messages":[]}`, wants: []string{`"id":"call_1"`, `"arguments":"{\"q\":"`, `"finish_reason":"tool_calls"`}},
		{path: "/v1/responses", body: `{"model":"grok-chat","stream":true,"input":"hello"}`, wants: []string{`event: response.output_item.added`, `"id":"item_1"`, `"item_id":"item_1"`, `event: response.function_call_arguments.done`, `"status":"completed"`}},
		{path: "/v1/messages", body: `{"model":"grok-chat","stream":true,"messages":[]}`, wants: []string{`"type":"tool_use"`, `"type":"input_json_delta"`, `"partial_json":"{\"q\":"`, `"stop_reason":"tool_use"`}},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			body := response.Body.String()
			for _, want := range test.wants {
				if !strings.Contains(body, want) {
					t.Fatalf("stream missing %q:\n%s", want, body)
				}
			}
			if strings.Count(body, `"type":"tool_use"`) != map[bool]int{true: 1, false: 0}[test.path == "/v1/messages"] {
				t.Fatalf("unexpected tool_use block count:\n%s", body)
			}
		})
	}
}

func TestResponsesAndAnthropicNonStreaming(t *testing.T) {
	client := upstream.ClientFunc(func(_ context.Context, _ upstream.Request) (*upstream.Response, error) {
		return &upstream.Response{StatusCode: http.StatusOK, Events: upstream.Events(
			upstream.Event{Kind: upstream.EventReasoningDelta, Text: "think"},
			upstream.Event{Kind: upstream.EventTextDelta, Text: "answer"},
			upstream.Event{Kind: upstream.EventToolCall, ID: "call_1", Name: "lookup", Arguments: `{"q":"x"}`},
			upstream.Event{Kind: upstream.EventUsage, Usage: upstream.Usage{InputTokens: 3, OutputTokens: 4, CachedTokens: 2}},
			upstream.Event{Kind: upstream.EventDone},
		)}, nil
	})
	handler := testGateway(t, client)

	for _, test := range []struct {
		path string
		body string
		want string
	}{
		{"/v1/responses", `{"model":"grok-chat","input":"hello"}`, `"object":"response"`},
		{"/v1/messages", `{"model":"grok-chat","messages":[{"role":"user","content":"hello"}]}`, `"type":"message"`},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body)))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), test.want) || !strings.Contains(response.Body.String(), "call_1") {
			t.Fatalf("unexpected %s response: %d %s", test.path, response.Code, response.Body.String())
		}
	}
}

func TestImagePassThroughAndOpenAIErrorShape(t *testing.T) {
	client := upstream.ClientFunc(func(_ context.Context, request upstream.Request) (*upstream.Response, error) {
		if request.Operation == upstream.OperationImage {
			return &upstream.Response{StatusCode: http.StatusOK, Body: json.RawMessage(`{"created":1,"data":[{"url":"https://cdn.example/image.png"}]}`)}, nil
		}
		return &upstream.Response{StatusCode: http.StatusUnauthorized, Body: json.RawMessage(`{"error":{"message":"expired"}}`)}, nil
	})
	handler := testGateway(t, client)

	image := httptest.NewRecorder()
	handler.ServeHTTP(image, httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"grok-image","prompt":"x"}`)))
	if image.Code != http.StatusOK || !strings.Contains(image.Body.String(), "cdn.example") {
		t.Fatalf("unexpected image response: %d %s", image.Code, image.Body.String())
	}

	bad := httptest.NewRecorder()
	handler.ServeHTTP(bad, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"missing","messages":[]}`)))
	var payload map[string]map[string]any
	if json.Unmarshal(bad.Body.Bytes(), &payload) != nil || payload["error"]["type"] != "invalid_request_error" || payload["error"]["code"] != "model_not_found" {
		t.Fatalf("unexpected error body: %d %s", bad.Code, bad.Body.String())
	}
}

func TestImageEventPreservesBase64ResponseFormat(t *testing.T) {
	client := upstream.ClientFunc(func(_ context.Context, _ upstream.Request) (*upstream.Response, error) {
		return &upstream.Response{StatusCode: http.StatusOK, Events: upstream.Events(
			upstream.Event{Kind: upstream.EventImage, URL: "data:image/png;base64,aW1hZ2U="},
			upstream.Event{Kind: upstream.EventDone},
		)}, nil
	})
	handler := testGateway(t, client)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"grok-image","prompt":"x","response_format":"b64_json"}`))
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"b64_json":"aW1hZ2U="`) {
		t.Fatalf("base64 image response = %d %s", response.Code, response.Body.String())
	}
}

func TestGatewayFailsOverAcrossAllCredentialKindsBeforeWriting(t *testing.T) {
	models := NewStaticModelSource([]domain.ModelSpec{{
		ID: "grok-all", UpstreamModel: "grok-all", Capability: domain.CapabilityChat, Enabled: true,
		CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth, domain.CredentialConsoleSSO, domain.CredentialGrokSSO},
	}})
	items := []domain.Account{
		{ID: "01-cli", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, Priority: 300, ConcurrencyLimit: 1},
		{ID: "02-console", Kind: domain.CredentialConsoleSSO, Status: domain.AccountActive, Priority: 200, ConcurrencyLimit: 1},
		{ID: "03-grok", Kind: domain.CredentialGrokSSO, Status: domain.AccountActive, Priority: 100, ConcurrencyLimit: 1},
	}
	credentials := map[string]domain.Credentials{
		"01-cli":     {AccessToken: "cli"},
		"02-console": {SSO: "console"},
		"03-grok":    {SSO: "grok"},
	}
	store := accounts.NewMemoryStore(items, credentials)
	var called []domain.CredentialKind
	client := upstream.ClientFunc(func(_ context.Context, request upstream.Request) (*upstream.Response, error) {
		called = append(called, request.CredentialKind)
		switch request.CredentialKind {
		case domain.CredentialCLIOAuth:
			return &upstream.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"1"}}}, nil
		case domain.CredentialConsoleSSO:
			return &upstream.Response{StatusCode: http.StatusServiceUnavailable}, nil
		default:
			return &upstream.Response{StatusCode: http.StatusOK, Events: upstream.Events(
				upstream.Event{Kind: upstream.EventTextDelta, Text: "fallback answer"},
				upstream.Event{Kind: upstream.EventUsage, Usage: upstream.Usage{InputTokens: 2, OutputTokens: 2}},
				upstream.Event{Kind: upstream.EventDone},
			)}, nil
		}
	})
	handler, err := NewHandler(Config{Models: models, Accounts: accounts.NewPool(store, accounts.DefaultPolicy()), Upstream: client, MaxAttempts: 3})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"grok-all","messages":[]}`)))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "fallback answer") {
		t.Fatalf("unexpected failover response: %d %s", response.Code, response.Body.String())
	}
	want := []domain.CredentialKind{domain.CredentialCLIOAuth, domain.CredentialConsoleSSO, domain.CredentialGrokSSO}
	if !slices.Equal(called, want) {
		t.Fatalf("credential order = %v, want %v", called, want)
	}
}

func TestGatewayRefreshesCLIOAuthOnceBeforeFailover(t *testing.T) {
	models := NewStaticModelSource([]domain.ModelSpec{{
		ID: "grok-refresh", UpstreamModel: "grok-refresh", Capability: domain.CapabilityChat,
		CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}, Enabled: true,
	}})
	store := accounts.NewMemoryStore(
		[]domain.Account{{ID: "oauth", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, ConcurrencyLimit: 1}},
		map[string]domain.Credentials{"oauth": {AccessToken: "old-access", RefreshToken: "refresh"}},
	)
	pool := accounts.NewPool(store, accounts.DefaultPolicy())
	var calls []string
	client := upstream.ClientFunc(func(_ context.Context, request upstream.Request) (*upstream.Response, error) {
		calls = append(calls, request.Credentials.AccessToken)
		if request.Credentials.AccessToken == "old-access" {
			return &upstream.Response{StatusCode: http.StatusUnauthorized}, nil
		}
		return &upstream.Response{StatusCode: http.StatusOK, Events: upstream.Events(
			upstream.Event{Kind: upstream.EventTextDelta, Text: "refreshed"},
			upstream.Event{Kind: upstream.EventDone},
		)}, nil
	})
	refreshes := 0
	handler, err := NewHandler(Config{
		Models: models, Accounts: pool, Upstream: client, MaxAttempts: 2,
		RefreshAccount: func(ctx context.Context, accountID string) error {
			refreshes++
			store.SetCredentials(accountID, domain.Credentials{AccessToken: "new-access", RefreshToken: "refresh"})
			return pool.Reload(ctx)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"grok-refresh","messages":[]}`)))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "refreshed") || refreshes != 1 || !slices.Equal(calls, []string{"old-access", "new-access"}) {
		t.Fatalf("refresh retry = status:%d refreshes:%d calls:%v body:%s", response.Code, refreshes, calls, response.Body.String())
	}
}

func TestImageAndImageEditCapabilitiesUseDistinctEndpoints(t *testing.T) {
	image := domain.ModelSpec{Capability: domain.CapabilityImage}
	edit := domain.ModelSpec{Capability: domain.CapabilityImageEdit}
	if !allowsOperation(image, upstream.OperationImage) || allowsOperation(image, upstream.OperationImageEdit) {
		t.Fatal("image generation capability accepted the wrong media operation")
	}
	if !allowsOperation(edit, upstream.OperationImageEdit) || allowsOperation(edit, upstream.OperationImage) {
		t.Fatal("image edit capability accepted the wrong media operation")
	}
	if !allowsOperation(edit, upstream.OperationChat) {
		t.Fatal("image edit capability must remain available through chat completions")
	}
}

func TestQuotaFromHeadersPreservesLimitsUnlimitedAndReset(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	header := make(http.Header)
	header.Set("X-RateLimit-Limit-Requests", "100")
	header.Set("X-RateLimit-Remaining-Requests", "40")
	header.Set("X-RateLimit-Limit-Tokens", "unlimited")
	header.Set("X-RateLimit-Reset-Requests", "90s")
	quota := quotaFromHeaders(header, now)
	if quota == nil || quota.RequestsLimit != 100 || quota.RequestsRemaining != 40 || !quota.TokensUnlimited || quota.ResetAt == nil || !quota.ResetAt.Equal(now.Add(90*time.Second)) {
		t.Fatalf("quota headers = %+v", quota)
	}
	if quotaFromHeaders(make(http.Header), now) != nil {
		t.Fatal("empty headers produced a quota snapshot")
	}
}

func TestQuotaFromHeadersUsesResetForExhaustedDimension(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	header := make(http.Header)
	header.Set("X-RateLimit-Limit-Requests", "100")
	header.Set("X-RateLimit-Remaining-Requests", "0")
	header.Set("X-RateLimit-Limit-Tokens", "1000")
	header.Set("X-RateLimit-Remaining-Tokens", "500")
	header.Set("X-RateLimit-Reset-Requests", "30s")
	header.Set("X-RateLimit-Reset-Tokens", "1h")
	quota := quotaFromHeaders(header, now)
	if quota == nil || quota.ResetAt == nil || !quota.ResetAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("request-exhausted reset = %+v", quota)
	}

	header.Set("X-RateLimit-Remaining-Tokens", "0")
	quota = quotaFromHeaders(header, now)
	if quota == nil || quota.ResetAt == nil || !quota.ResetAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("both-dimensions-exhausted reset = %+v", quota)
	}
}
