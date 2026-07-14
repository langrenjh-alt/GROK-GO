package upstream

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

func TestCLIOAuthAdapterAppliesFixedProtocolHeaders(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", nil)
	if err := (CLIOAuthAdapter{}).Apply(request, domain.Credentials{AccessToken: "test-access-token"}); err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"X-XAI-Token-Auth":         "xai-grok-cli",
		"X-Grok-Client-Version":    "0.2.93",
		"X-Grok-Client-Identifier": "grok-shell",
		"User-Agent":               "xai-grok-workspace/0.2.93",
	}
	for name, value := range want {
		if got := request.Header.Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
}

func TestCLIOAuthAdapterHonorsExplicitUserAgent(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "https://cli-chat-proxy.grok.com/v1/responses", nil)
	if err := (CLIOAuthAdapter{}).Apply(request, domain.Credentials{AccessToken: "test-access-token", UserAgent: "grok-cli/0.2.93"}); err != nil {
		t.Fatal(err)
	}
	if got := request.Header.Get("User-Agent"); got != "grok-cli/0.2.93" {
		t.Fatalf("User-Agent = %q, want grok-cli/0.2.93", got)
	}
}
