package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/langrenjh-alt/GROK-GO/internal/accounts"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/upstream"
)

type localizerFunc func(context.Context, string, string) (string, error)

func (f localizerFunc) Localize(ctx context.Context, kind, rawURL string) (string, error) {
	return f(ctx, kind, rawURL)
}

type base64Localizer struct {
	localize func(context.Context, string, string) (string, error)
	encode   func(context.Context, string, string) (string, string, error)
}

func (l base64Localizer) Localize(ctx context.Context, kind, rawURL string) (string, error) {
	return l.localize(ctx, kind, rawURL)
}

func (l base64Localizer) LocalizeBase64(ctx context.Context, kind, rawURL string) (string, string, error) {
	return l.encode(ctx, kind, rawURL)
}

func mediaGateway(t *testing.T, client upstream.Client, localizer MediaLocalizer) http.Handler {
	t.Helper()
	models := NewStaticModelSource([]domain.ModelSpec{
		{ID: "grok-chat", UpstreamModel: "grok-chat", Capability: domain.CapabilityChat, CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}, Enabled: true},
		{ID: "grok-image", UpstreamModel: "grok-image", Capability: domain.CapabilityImage, CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}, Enabled: true},
	})
	store := accounts.NewMemoryStore([]domain.Account{{ID: "a", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, ConcurrencyLimit: 2}}, map[string]domain.Credentials{"a": {AccessToken: "token"}})
	handler, err := NewHandler(Config{Models: models, Accounts: accounts.NewPool(store, accounts.DefaultPolicy()), Upstream: client, Media: localizer})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func TestNonStreamingImageUsesLocalizedURL(t *testing.T) {
	client := upstream.ClientFunc(func(context.Context, upstream.Request) (*upstream.Response, error) {
		return &upstream.Response{StatusCode: http.StatusOK, Body: json.RawMessage(`{"created":1,"data":[{"url":"https://cdn.test/image.png"}]}`)}, nil
	})
	handler := mediaGateway(t, client, localizerFunc(func(_ context.Context, kind, rawURL string) (string, error) {
		if kind != "image" || rawURL != "https://cdn.test/image.png" {
			t.Fatalf("unexpected localization: %s %s", kind, rawURL)
		}
		return "https://gateway.test/media/local?sig=x", nil
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"grok-image","prompt":"x"}`)))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "gateway.test/media/local") || strings.Contains(response.Body.String(), "cdn.test") {
		t.Fatalf("unexpected localized response: %d %s", response.Code, response.Body.String())
	}
}

func TestStreamingMediaEventUsesLocalizedURL(t *testing.T) {
	client := upstream.ClientFunc(func(context.Context, upstream.Request) (*upstream.Response, error) {
		return &upstream.Response{StatusCode: http.StatusOK, Events: upstream.Events(upstream.Event{Kind: upstream.EventImage, URL: "https://cdn.test/image.png"}, upstream.Event{Kind: upstream.EventDone})}, nil
	})
	handler := mediaGateway(t, client, localizerFunc(func(context.Context, string, string) (string, error) {
		return "https://gateway.test/media/local?sig=x", nil
	}))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"grok-chat","stream":true,"messages":[]}`)))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "gateway.test/media/local") || strings.Contains(response.Body.String(), "cdn.test") {
		t.Fatalf("unexpected localized stream: %d %s", response.Code, response.Body.String())
	}
}

func TestNonStreamingImageURLUsesRequestedBase64Format(t *testing.T) {
	client := upstream.ClientFunc(func(context.Context, upstream.Request) (*upstream.Response, error) {
		return &upstream.Response{StatusCode: http.StatusOK, Events: upstream.Events(upstream.Event{Kind: upstream.EventImage, URL: "https://cdn.test/image.png"}, upstream.Event{Kind: upstream.EventDone})}, nil
	})
	localizer := base64Localizer{
		localize: func(context.Context, string, string) (string, error) {
			t.Fatal("base64 image response must use the combined cache path")
			return "", nil
		},
		encode: func(_ context.Context, kind, rawURL string) (string, string, error) {
			if kind != "image" || rawURL != "https://cdn.test/image.png" {
				t.Fatalf("unexpected base64 localization: %s %s", kind, rawURL)
			}
			return "https://gateway.test/media/local?sig=x", "aW1hZ2UtYnl0ZXM=", nil
		},
	}
	handler := mediaGateway(t, client, localizer)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/v1/images/generations", strings.NewReader(`{"model":"grok-image","prompt":"x","response_format":"b64_json"}`)))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"b64_json":"aW1hZ2UtYnl0ZXM="`) || strings.Contains(response.Body.String(), `"url"`) {
		t.Fatalf("unexpected base64 response: %d %s", response.Code, response.Body.String())
	}
}

func TestVideoStatusURLIsNotDownloadedAsContent(t *testing.T) {
	handler := &Handler{config: Config{Media: localizerFunc(func(context.Context, string, string) (string, error) {
		t.Fatal("status URL must not be localized as video content")
		return "", nil
	})}}
	body := []byte(`{"id":"video_1","status":"queued","status_url":"https://api.test/videos/video_1"}`)
	localized, err := handler.localizeBody(context.Background(), upstream.OperationVideo, body)
	if err != nil {
		t.Fatal(err)
	}
	if string(localized) != string(body) {
		var got, want any
		_ = json.Unmarshal(localized, &got)
		_ = json.Unmarshal(body, &want)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("localized status payload = %s", localized)
		}
	}
}
