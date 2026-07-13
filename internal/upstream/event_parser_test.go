package upstream

import (
	"encoding/json"
	"testing"
)

func TestSSEParserKeepsFunctionCallIdentityAcrossEvents(t *testing.T) {
	parser := newSSEParser()
	added := parser.parse("response.output_item.added", []byte(`{"output_index":2,"item":{"id":"item_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":""}}`))
	delta := parser.parse("response.function_call_arguments.delta", []byte(`{"item_id":"item_1","output_index":2,"delta":"{\"q\":"}`))
	delta = append(delta, parser.parse("response.function_call_arguments.delta", []byte(`{"item_id":"item_1","output_index":2,"delta":"\"x\"}"}`))...)
	done := parser.parse("response.output_item.done", []byte(`{"output_index":2,"item":{"id":"item_1","type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"x\"}"}}`))
	if len(added) != 1 || len(delta) != 2 || len(done) != 0 {
		t.Fatalf("unexpected event counts: added=%#v delta=%#v done=%#v", added, delta, done)
	}
	for _, event := range append(added, delta...) {
		if event.ID != "call_1" || event.Name != "lookup" || event.Index != 0 {
			t.Fatalf("tool identity drifted: %#v", event)
		}
	}
}

func TestSSEParserSeparatesGrokThinkingTextAndImage(t *testing.T) {
	parser := newSSEParser()
	thinking := parser.parse("", []byte(`{"result":{"response":{"token":"plan","isThinking":true}}}`))
	text := parser.parse("", []byte(`{"result":{"response":{"token":"answer","isThinking":false,"messageTag":"final"}}}`))
	image := parser.parse("", []byte(`{"result":{"response":{"cardAttachment":{"jsonData":"{\"image_chunk\":{\"progress\":100,\"imageUrl\":\"generated/image.png\",\"moderated\":false}}"}}}}`))
	done := parser.parse("", []byte(`{"result":{"response":{"finalMetadata":{"usage":{"input_tokens":1}}}}}`))
	if len(thinking) != 1 || thinking[0].Kind != EventReasoningDelta || thinking[0].Text != "plan" {
		t.Fatalf("thinking event = %#v", thinking)
	}
	if len(text) != 1 || text[0].Kind != EventTextDelta || text[0].Text != "answer" {
		t.Fatalf("text event = %#v", text)
	}
	if len(image) != 1 || image[0].URL != "https://assets.grok.com/generated/image.png" {
		t.Fatalf("image event = %#v", image)
	}
	if len(done) != 2 || done[0].Kind != EventUsage || done[0].Usage.InputTokens != 1 || done[1].Kind != EventDone {
		t.Fatalf("done event = %#v", done)
	}
}

func TestParseNonStreamPreservesReasoningToolsImagesAndCacheUsage(t *testing.T) {
	payload := map[string]any{
		"output": []any{
			map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "plan"}}},
			map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "answer"}}},
			map[string]any{"type": "function_call", "call_id": "call_1", "name": "lookup", "arguments": `{"q":"x"}`},
			map[string]any{"type": "image_generation_call", "id": "image_1", "result": "aW1hZ2U=", "output_format": "png"},
		},
		"usage": map[string]any{"input_tokens": float64(10), "output_tokens": float64(4), "input_tokens_details": map[string]any{"cached_tokens": float64(7)}},
	}
	body, _ := json.Marshal(payload)
	events := parseNonStream(body)
	seen := map[EventKind]bool{}
	var usage Usage
	for _, event := range events {
		seen[event.Kind] = true
		if event.Kind == EventUsage {
			usage = event.Usage
		}
	}
	for _, kind := range []EventKind{EventReasoningDelta, EventTextDelta, EventToolCall, EventImage, EventUsage, EventDone} {
		if !seen[kind] {
			t.Fatalf("missing %s in %#v", kind, events)
		}
	}
	if usage.InputTokens != 10 || usage.OutputTokens != 4 || usage.CachedTokens != 7 {
		t.Fatalf("usage = %+v", usage)
	}
}
