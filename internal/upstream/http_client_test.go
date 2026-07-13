package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

type observedReadCloser struct {
	reader io.Reader
	closed chan struct{}
	once   sync.Once
}

func (r *observedReadCloser) Read(buffer []byte) (int, error) {
	if r.reader != nil {
		return r.reader.Read(buffer)
	}
	<-r.closed
	return 0, io.ErrClosedPipe
}

func (r *observedReadCloser) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}

func TestHTTPClientAdaptsConsoleAndParsesSSE(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer anonymous" || !strings.Contains(r.Header.Get("Cookie"), "sso=secret") {
			t.Errorf("unexpected auth headers: %#v", r.Header)
		}
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		if payload["model"] != "grok-upstream" || payload["input"] == nil {
			t.Errorf("unexpected payload: %s", body)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"response\":{\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n")
	}))
	defer server.Close()
	client := NewHTTPClient(HTTPClientConfig{Client: server.Client(), ConsoleBaseURL: server.URL})
	response, err := client.Do(context.Background(), Request{Operation: OperationChat, Model: "public", UpstreamModel: "grok-upstream", CredentialKind: domain.CredentialConsoleSSO, Credentials: domain.Credentials{SSO: "secret"}, Body: json.RawMessage(`{"messages":[{"role":"user","content":"hi"}]}`), Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	for event := range response.Events {
		events = append(events, event)
	}
	if len(events) < 3 || events[0].Text != "hello" || events[1].Usage.InputTokens != 2 || events[2].Kind != EventDone {
		t.Fatalf("unexpected events: %#v", events)
	}
}

func TestCredentialAdaptersAreDistinct(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "https://example.test", nil)
	if err := (GrokSSOAdapter{}).Apply(request, domain.Credentials{SSO: "a", SSORW: "b", CFClearance: "c"}); err != nil {
		t.Fatal(err)
	}
	if request.Header.Get("Authorization") != "" || !strings.Contains(request.Header.Get("Cookie"), "sso-rw=b") {
		t.Fatalf("unexpected Grok headers: %#v", request.Header)
	}
	request = httptest.NewRequest(http.MethodPost, "https://example.test", nil)
	if err := (CLIOAuthAdapter{}).Apply(request, domain.Credentials{AccessToken: "access"}); err != nil {
		t.Fatal(err)
	}
	if request.Header.Get("Authorization") != "Bearer access" || request.Header.Get("Cookie") != "" {
		t.Fatalf("unexpected CLI headers: %#v", request.Header)
	}
}

func TestGrokModeAcceptsCatalogMappings(t *testing.T) {
	for input, want := range map[string]string{
		"auto": "auto", "fast": "fast", "expert": "expert", "heavy": "heavy",
		"grok-420-computer-use-sa": "grok-420-computer-use-sa",
		"grok-4.20-0309-reasoning": "expert",
	} {
		if got := grokMode(input); got != want {
			t.Errorf("grokMode(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestHTTPClientUsesVideoGenerationAndStatusRoutes(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" "+r.URL.EscapedPath())
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"request 123","status":"queued"}`)
	}))
	defer server.Close()
	client := NewHTTPClient(HTTPClientConfig{Client: server.Client(), CLIBaseURL: server.URL + "/v1"})
	credentials := domain.Credentials{AccessToken: "access"}
	if _, err := client.Do(context.Background(), Request{Operation: OperationVideo, CredentialKind: domain.CredentialCLIOAuth, Credentials: credentials, Body: json.RawMessage(`{"model":"grok-video","prompt":"waves"}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Do(context.Background(), Request{Operation: OperationVideoStatus, CredentialKind: domain.CredentialCLIOAuth, Credentials: credentials, VideoID: "request 123"}); err != nil {
		t.Fatal(err)
	}
	want := []string{"POST /v1/videos/generations", "GET /v1/videos/request%20123"}
	if len(requests) != len(want) || requests[0] != want[0] || requests[1] != want[1] {
		t.Fatalf("video requests = %v, want %v", requests, want)
	}
}

func TestVideoStatusEndpointRejectsAnotherOrigin(t *testing.T) {
	if _, err := videoStatusEndpoint("https://api.test/v1", "https://other.test/status/1", "1"); err == nil {
		t.Fatal("cross-origin video status URL was accepted")
	}
}

func TestHTTPClientReusesNormalizedProxyClients(t *testing.T) {
	client := NewHTTPClient(HTTPClientConfig{MaxProxyClients: 2})
	first, err := client.clientForProxy("HTTP://Proxy.Example.Test/")
	if err != nil {
		t.Fatal(err)
	}
	reused, err := client.clientForProxy("http://proxy.example.test:80")
	if err != nil {
		t.Fatal(err)
	}
	isolated, err := client.clientForProxy("http://other.example.test:80")
	if err != nil {
		t.Fatal(err)
	}
	if first != reused {
		t.Fatal("equivalent proxy URLs did not reuse the same client")
	}
	if first == isolated {
		t.Fatal("different proxies shared a client")
	}
	if _, err := client.clientForProxy("socks5://third.example.test:1080"); err != nil {
		t.Fatal(err)
	}
	if len(client.proxyClients) != 2 {
		t.Fatalf("proxy cache size = %d, want 2", len(client.proxyClients))
	}
	firstAfterEviction, err := client.clientForProxy("http://proxy.example.test:80")
	if err != nil {
		t.Fatal(err)
	}
	if firstAfterEviction == first {
		t.Fatal("least-recently-used proxy client was not evicted")
	}
}

func TestHTTPClientUsesTunedDirectTransport(t *testing.T) {
	client := NewHTTPClient(HTTPClientConfig{})
	transport, ok := client.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("direct transport type = %T", client.client.Transport)
	}
	if transport.MaxIdleConns != 512 || transport.MaxIdleConnsPerHost != 128 {
		t.Fatalf("direct transport idle limits = %d/%d", transport.MaxIdleConns, transport.MaxIdleConnsPerHost)
	}
	if transport.Proxy != nil {
		t.Fatal("direct transport inherited an environment proxy")
	}
}

func TestHTTPClientReadsDynamicRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"output_text":"late"}`)
	}))
	defer server.Close()
	timeout := 20 * time.Millisecond
	client := NewHTTPClient(HTTPClientConfig{Client: server.Client(), CLIBaseURL: server.URL, RequestTimeoutFunc: func() time.Duration { return timeout }})
	_, err := client.Do(context.Background(), Request{Operation: OperationResponses, CredentialKind: domain.CredentialCLIOAuth, Credentials: domain.Credentials{AccessToken: "access"}, Body: json.RawMessage(`{"model":"grok"}`)})
	if err == nil {
		t.Fatal("request completed despite dynamic timeout")
	}
}

func TestHTTPClientTimeoutRemainsActiveForStreamLifetime(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		time.Sleep(30 * time.Millisecond)
		_, _ = io.WriteString(w, "event: response.output_text.delta\ndata: {\"delta\":\"hello\"}\n\n")
		_, _ = io.WriteString(w, "event: response.completed\ndata: {}\n\n")
	}))
	defer server.Close()
	client := NewHTTPClient(HTTPClientConfig{Client: server.Client(), CLIBaseURL: server.URL, RequestTimeoutFunc: func() time.Duration { return time.Second }})
	response, err := client.Do(context.Background(), Request{Operation: OperationResponses, CredentialKind: domain.CredentialCLIOAuth, Credentials: domain.Credentials{AccessToken: "access"}, Body: json.RawMessage(`{"model":"grok","stream":true}`), Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	for event := range response.Events {
		text += event.Text
	}
	if text != "hello" {
		t.Fatalf("stream text = %q", text)
	}
}

func TestStreamEventsCancellationUnblocksFullOutputChannel(t *testing.T) {
	var source strings.Builder
	for range 128 {
		source.WriteString("event: response.output_text.delta\n")
		source.WriteString("data: {\"delta\":\"token\"}\n\n")
	}
	body := &observedReadCloser{reader: strings.NewReader(source.String()), closed: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	events := streamEvents(ctx, body)

	deadline := time.Now().Add(time.Second)
	for len(events) < cap(events) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(events) != cap(events) {
		t.Fatalf("stream output buffer did not fill: %d/%d", len(events), cap(events))
	}
	cancel()

	select {
	case <-body.closed:
	case <-time.After(time.Second):
		t.Fatal("response body was not closed after cancellation")
	}
	for {
		select {
		case _, ok := <-events:
			if !ok {
				return
			}
		case <-time.After(time.Second):
			t.Fatal("stream producer remained blocked after cancellation")
		}
	}
}

func TestStreamEventsCancellationClosesBodyBlockedInRead(t *testing.T) {
	body := &observedReadCloser{closed: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	events := streamEvents(ctx, body)
	cancel()

	select {
	case <-body.closed:
	case <-time.After(time.Second):
		t.Fatal("response body was not closed while scanner was blocked")
	}
	select {
	case _, ok := <-events:
		if ok {
			t.Fatal("canceled stream emitted an unexpected event")
		}
	case <-time.After(time.Second):
		t.Fatal("stream producer did not stop after closing the body")
	}
}
