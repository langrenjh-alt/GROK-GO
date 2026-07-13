package gateway

import (
	"net/http"
	"regexp"
	"testing"
)

func TestPromptCacheIdentityStableAndTenantIsolated(t *testing.T) {
	payload := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "Be concise"},
			map[string]any{"role": "user", "content": "Hello"},
		},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}}},
	}
	firstHeaders := http.Header{"Authorization": []string{"Bearer tenant-a"}}
	secondHeaders := http.Header{"Authorization": []string{"Bearer tenant-b"}}
	first := promptCacheIdentity(firstHeaders, "grok-4.5", payload)
	if repeated := promptCacheIdentity(firstHeaders, "grok-4.5", payload); repeated != first {
		t.Fatalf("identity changed: %q != %q", repeated, first)
	}
	if other := promptCacheIdentity(secondHeaders, "grok-4.5", payload); other == first {
		t.Fatal("different tenants shared a prompt cache identity")
	}
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(first) {
		t.Fatalf("identity is not UUID shaped: %q", first)
	}
}

func TestPromptCacheIdentityUsesAuthenticatedCredentialPrecedence(t *testing.T) {
	payload := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "Hello"}}}
	firstHeaders := http.Header{"Authorization": []string{"Bearer shared-decoy"}, "X-Api-Key": []string{"tenant-a"}}
	secondHeaders := http.Header{"Authorization": []string{"Bearer shared-decoy"}, "X-Api-Key": []string{"tenant-b"}}
	if first, second := promptCacheIdentity(firstHeaders, "grok", payload), promptCacheIdentity(secondHeaders, "grok", payload); first == second {
		t.Fatal("different authenticated X-API-Key tenants shared a prompt cache identity")
	}
}

func TestPromptCacheIdentityPrefersSessionAndAnthropicCacheAnchor(t *testing.T) {
	payload := map[string]any{
		"system":   []any{map[string]any{"type": "text", "text": "cached system", "cache_control": map[string]any{"type": "ephemeral"}}},
		"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "cached user", "cache_control": map[string]any{"type": "ephemeral"}}}}},
	}
	headers := http.Header{"Authorization": []string{"Bearer tenant"}}
	anchored := promptCacheIdentity(headers, "grok", payload)
	changed := map[string]any{
		"system": payload["system"],
		"messages": []any{
			map[string]any{"role": "user", "content": payload["messages"].([]any)[0].(map[string]any)["content"]},
			map[string]any{"role": "user", "content": "later turn"},
		},
	}
	if got := promptCacheIdentity(headers, "grok", changed); got != anchored {
		t.Fatalf("cache anchor changed across later turns: %q != %q", got, anchored)
	}

	sessionHeaders := headers.Clone()
	sessionHeaders.Set("X-Claude-Code-Session-Id", "session-123")
	first := promptCacheIdentity(sessionHeaders, "grok", payload)
	if got := promptCacheIdentity(sessionHeaders, "grok", map[string]any{"messages": []any{map[string]any{"role": "user", "content": "different"}}}); got != first {
		t.Fatalf("explicit session did not dominate request content: %q != %q", got, first)
	}
}
