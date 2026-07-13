package upstream

import (
	"encoding/json"
	"testing"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

func TestPreparePayloadConvertsChatToolsAndMultimodalHistory(t *testing.T) {
	body := json.RawMessage(`{
  "messages": [
    {"role":"system","content":"Be concise"},
    {"role":"user","content":[{"type":"text","text":"inspect"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AA=="}}]},
    {"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},
    {"role":"tool","tool_call_id":"call_1","content":[{"type":"text","text":"done"}]}
  ],
  "tools":[{"type":"function","function":{"name":"lookup","description":"Lookup","parameters":{"type":"object"}}}],
  "tool_choice":{"type":"function","function":{"name":"lookup"}},
  "reasoning_effort":"high"
}`)
	encoded, err := preparePayload(Request{
		Operation: OperationChat, Model: "grok-public", UpstreamModel: "grok-4.5",
		PromptCacheKey: "11111111-1111-5111-8111-111111111111",
		CredentialKind: domain.CredentialCLIOAuth, Body: body, Stream: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatal(err)
	}
	input := valueArray(payload["input"])
	if len(input) != 4 {
		t.Fatalf("input item count = %d, payload=%s", len(input), encoded)
	}
	developer := input[0].(map[string]any)
	if developer["role"] != "developer" {
		t.Fatalf("system role was not normalized: %#v", developer)
	}
	userContent := valueArray(input[1].(map[string]any)["content"])
	if len(userContent) != 2 || userContent[1].(map[string]any)["type"] != "input_image" {
		t.Fatalf("multimodal content was not preserved: %#v", userContent)
	}
	if input[2].(map[string]any)["type"] != "function_call" || input[3].(map[string]any)["type"] != "function_call_output" {
		t.Fatalf("tool history was not normalized: %#v", input)
	}
	tools := valueArray(payload["tools"])
	function := tools[0].(map[string]any)
	if function["name"] != "lookup" || function["function"] != nil {
		t.Fatalf("tool schema was not flattened: %#v", function)
	}
	choice := payload["tool_choice"].(map[string]any)
	if choice["name"] != "lookup" || payload["prompt_cache_key"] == "" {
		t.Fatalf("tool choice/cache key mismatch: %#v", payload)
	}
}

func TestPreparePayloadConvertsAnthropicToolBlocks(t *testing.T) {
	body := json.RawMessage(`{
  "system":[{"type":"text","text":"Use tools","cache_control":{"type":"ephemeral"}}],
  "messages":[
    {"role":"assistant","content":[{"type":"thinking","thinking":"plan"},{"type":"tool_use","id":"tool_1","name":"search","input":{"q":"grok"}}]},
    {"role":"user","content":[{"type":"tool_result","tool_use_id":"tool_1","content":"result"}]}
  ],
  "tools":[{"name":"search","description":"Search","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}],
  "tool_choice":{"type":"tool","name":"search"},
  "max_tokens":4096
}`)
	encoded, err := preparePayload(Request{Operation: OperationMessages, Model: "grok", UpstreamModel: "grok-4.5", CredentialKind: domain.CredentialCLIOAuth, Body: body})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	_ = json.Unmarshal(encoded, &payload)
	input := valueArray(payload["input"])
	foundCall, foundOutput := false, false
	for _, raw := range input {
		item := raw.(map[string]any)
		if item["type"] == "message" && len(valueArray(item["content"])) == 0 {
			t.Fatalf("anthropic tool history contains an empty message item: %s", encoded)
		}
		foundCall = foundCall || item["type"] == "function_call"
		foundOutput = foundOutput || item["type"] == "function_call_output"
	}
	if !foundCall || !foundOutput || payload["instructions"] != "Use tools" || payload["max_output_tokens"].(float64) != 4096 {
		t.Fatalf("anthropic request was not normalized: %s", encoded)
	}
	tool := valueArray(payload["tools"])[0].(map[string]any)
	if tool["type"] != "function" || tool["parameters"] == nil {
		t.Fatalf("anthropic tool was not converted: %#v", tool)
	}
}

func TestPreparePayloadAddsCacheRoutingOnlyWithoutToolIntent(t *testing.T) {
	encoded, err := preparePayload(Request{
		Operation: OperationResponses, Model: "grok", UpstreamModel: "grok-4.5",
		PromptCacheKey: "22222222-2222-5222-8222-222222222222",
		CredentialKind: domain.CredentialCLIOAuth, Body: json.RawMessage(`{"input":"hello"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	_ = json.Unmarshal(encoded, &payload)
	if payload["tool_choice"] != "none" || len(valueArray(payload["tools"])) != 2 {
		t.Fatalf("cache-capable native routing was not added: %s", encoded)
	}

	encoded, err = preparePayload(Request{
		Operation: OperationResponses, Model: "grok", UpstreamModel: "grok-4.5",
		PromptCacheKey: "22222222-2222-5222-8222-222222222222",
		CredentialKind: domain.CredentialCLIOAuth, Body: json.RawMessage(`{"input":"hello","tools":[]}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	payload = nil
	_ = json.Unmarshal(encoded, &payload)
	if _, ok := payload["tools"]; ok {
		t.Fatalf("explicit tool intent was replaced with native routing: %s", encoded)
	}
}

func TestPrepareConsolePayloadAppliesModelDefaults(t *testing.T) {
	encoded, err := preparePayload(Request{
		Operation: OperationChat, Model: "grok-4.3-high", UpstreamModel: "grok-4.3",
		CredentialKind: domain.CredentialConsoleSSO, Body: json.RawMessage(`{"messages":[{"role":"user","content":"hello"}]}`), Stream: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	_ = json.Unmarshal(encoded, &payload)
	reasoning := payload["reasoning"].(map[string]any)
	if reasoning["effort"] != "high" || payload["max_output_tokens"].(float64) != 1_000_000 || payload["tool_choice"] != "auto" {
		t.Fatalf("console defaults missing: %s", encoded)
	}
}
