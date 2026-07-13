package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

func TestPreparePayloadShortensCollidingToolNames(t *testing.T) {
	first := strings.Repeat("a", 70) + "first"
	second := strings.Repeat("a", 70) + "second"
	body, _ := json.Marshal(map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
		"tools": []any{
			map[string]any{"type": "function", "function": map[string]any{"name": first, "parameters": map[string]any{"type": "object"}}},
			map[string]any{"type": "function", "function": map[string]any{"name": second, "parameters": map[string]any{"type": "object"}}},
		},
		"tool_choice": map[string]any{"type": "function", "function": map[string]any{"name": second}},
	})
	encoded, err := preparePayload(Request{Operation: OperationChat, CredentialKind: domain.CredentialCLIOAuth, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	_ = json.Unmarshal(encoded, &payload)
	tools := valueArray(payload["tools"])
	firstAlias := stringValue(tools[0].(map[string]any), "name")
	secondAlias := stringValue(tools[1].(map[string]any), "name")
	if firstAlias == secondAlias || utf8.RuneCountInString(firstAlias) > 64 || utf8.RuneCountInString(secondAlias) > 64 {
		t.Fatalf("aliases = %q, %q", firstAlias, secondAlias)
	}
	choice := payload["tool_choice"].(map[string]any)
	if choice["name"] != secondAlias {
		t.Fatalf("tool choice alias = %#v, want %q", choice, secondAlias)
	}
}

func TestHTTPClientRestoresToolNameFromStream(t *testing.T) {
	original := "mcp__very_long_server_name__" + strings.Repeat("lookup_records_", 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		tool := valueArray(payload["tools"])[0].(map[string]any)
		alias := stringValue(tool, "name")
		if alias == original || utf8.RuneCountInString(alias) > 64 {
			t.Errorf("tool was not shortened: %q", alias)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: response.output_item.added\ndata: {\"output_index\":0,\"item\":{\"id\":\"item_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":%q,\"arguments\":\"{}\"}}\n\n", alias)
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"response\":{}}\n\n")
	}))
	defer server.Close()
	body, _ := json.Marshal(map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "hello"}},
		"tools":    []any{map[string]any{"type": "function", "function": map[string]any{"name": original, "parameters": map[string]any{"type": "object"}}}},
	})
	client := NewHTTPClient(HTTPClientConfig{Client: server.Client(), CLIBaseURL: server.URL})
	response, err := client.Do(context.Background(), Request{Operation: OperationChat, Model: "grok", UpstreamModel: "grok", CredentialKind: domain.CredentialCLIOAuth, Credentials: domain.Credentials{AccessToken: "token"}, Body: body, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	var toolEvent Event
	for event := range response.Events {
		if event.Kind == EventToolCall {
			toolEvent = event
		}
	}
	if toolEvent.Name != original {
		t.Fatalf("restored tool name = %q, want %q", toolEvent.Name, original)
	}
}
