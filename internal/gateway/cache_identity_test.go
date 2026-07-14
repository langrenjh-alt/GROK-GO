package gateway

import (
	"net/http"
	"regexp"
	"testing"
)

func TestCacheIdentitiesStableAndTenantIsolated(t *testing.T) {
	payload := map[string]any{
		"messages": []any{
			map[string]any{"role": "system", "content": "Be concise"},
			map[string]any{"role": "user", "content": "Hello"},
		},
		"tools": []any{map[string]any{"type": "function", "function": map[string]any{"name": "lookup"}}},
	}
	first := resolveCacheIdentities("key-a", nil, "grok-4.5", payload)
	repeated := resolveCacheIdentities("key-a", nil, "grok-4.5", payload)
	if repeated != first {
		t.Fatalf("identities changed: %#v != %#v", repeated, first)
	}
	other := resolveCacheIdentities("key-b", nil, "grok-4.5", payload)
	if other.SessionAffinityKey == first.SessionAffinityKey || other.PromptCacheKey == first.PromptCacheKey {
		t.Fatal("different authenticated client keys shared a cache identity")
	}
	for name, value := range map[string]string{"session affinity": first.SessionAffinityKey, "prompt cache": first.PromptCacheKey} {
		if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(value) {
			t.Fatalf("%s identity is not UUID shaped: %q", name, value)
		}
	}
}

func TestCacheIdentitiesUseAuthenticatedKeyIDAcrossHeaderStyles(t *testing.T) {
	payload := map[string]any{"messages": []any{map[string]any{"role": "user", "content": "Hello"}}}
	bearer := http.Header{"Authorization": []string{"Bearer secret"}}
	xAPIKey := http.Header{"X-Api-Key": []string{"secret"}}
	first := resolveCacheIdentities("key-1", bearer, "grok", payload)
	second := resolveCacheIdentities("key-1", xAPIKey, "grok", payload)
	if first != second {
		t.Fatalf("the same authenticated key ID was split by auth header style: %#v != %#v", first, second)
	}
	third := resolveCacheIdentities("key-2", bearer, "grok", payload)
	if third.SessionAffinityKey == first.SessionAffinityKey || third.PromptCacheKey == first.PromptCacheKey {
		t.Fatal("different authenticated key IDs shared a cache identity")
	}
}

func TestSessionAffinityPrefersSessionAndAnthropicCacheAnchor(t *testing.T) {
	payload := map[string]any{
		"system":   []any{map[string]any{"type": "text", "text": "cached system", "cache_control": map[string]any{"type": "ephemeral"}}},
		"messages": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "cached user", "cache_control": map[string]any{"type": "ephemeral"}}}}},
	}
	anchored := resolveCacheIdentities("key", nil, "grok", payload).SessionAffinityKey
	changed := map[string]any{
		"system": payload["system"],
		"messages": []any{
			map[string]any{"role": "user", "content": payload["messages"].([]any)[0].(map[string]any)["content"]},
			map[string]any{"role": "user", "content": "later turn"},
		},
	}
	if got := resolveCacheIdentities("key", nil, "grok", changed).SessionAffinityKey; got != anchored {
		t.Fatalf("cache anchor changed across later turns: %q != %q", got, anchored)
	}

	firstHeaders := http.Header{"X-Claude-Code-Session-Id": []string{"session-123"}}
	secondHeaders := http.Header{"Session-Id": []string{"session-123"}}
	first := resolveCacheIdentities("key", firstHeaders, "grok", payload).SessionAffinityKey
	second := resolveCacheIdentities("key", secondHeaders, "grok", map[string]any{"messages": []any{map[string]any{"role": "user", "content": "different"}}}).SessionAffinityKey
	if second != first {
		t.Fatalf("explicit session did not dominate request content: %q != %q", second, first)
	}
}

func TestPromptCacheIdentityUsesStaticPrefixWithoutFirstUser(t *testing.T) {
	tools := []any{map[string]any{
		"type":     "function",
		"function": map[string]any{"name": "lookup", "parameters": map[string]any{"type": "object"}},
	}}
	first := resolveCacheIdentities("key", nil, "grok", map[string]any{
		"instructions": "Be concise",
		"tools":        tools,
		"messages":     []any{map[string]any{"role": "user", "content": "First conversation"}},
	})
	second := resolveCacheIdentities("key", nil, "grok", map[string]any{
		"messages": []any{
			map[string]any{"role": "developer", "content": []any{map[string]any{"type": "text", "text": "Be concise"}}},
			map[string]any{"role": "user", "content": "Second conversation"},
		},
		"tools": tools,
	})
	if first.PromptCacheKey != second.PromptCacheKey {
		t.Fatalf("equivalent normalized static prefixes produced different prompt keys: %q != %q", first.PromptCacheKey, second.PromptCacheKey)
	}
	if first.SessionAffinityKey == second.SessionAffinityKey {
		t.Fatal("different conversations shared the session affinity fallback")
	}
}

func TestPromptCacheIdentityFallsBackToFirstUserWithoutStaticPrefix(t *testing.T) {
	first := resolveCacheIdentities("key", nil, "grok", map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "Hello"}},
	})
	laterTurn := resolveCacheIdentities("key", nil, "grok", map[string]any{
		"messages": []any{
			map[string]any{"role": "user", "content": "Hello"},
			map[string]any{"role": "assistant", "content": "Hi"},
			map[string]any{"role": "user", "content": "Continue"},
		},
	})
	other := resolveCacheIdentities("key", nil, "grok", map[string]any{
		"messages": []any{map[string]any{"role": "user", "content": "Different"}},
	})
	if laterTurn.PromptCacheKey != first.PromptCacheKey {
		t.Fatal("first-user prompt cache fallback changed across later turns")
	}
	if other.PromptCacheKey == first.PromptCacheKey {
		t.Fatal("different first users shared a prompt key without a static prefix")
	}
}

func TestExplicitSessionsDoNotFragmentStaticPromptCache(t *testing.T) {
	payload := map[string]any{
		"system":   "Shared system prompt",
		"messages": []any{map[string]any{"role": "user", "content": "Hello"}},
	}
	first := resolveCacheIdentities("key", http.Header{"Session-Id": []string{"session-a"}}, "grok", payload)
	second := resolveCacheIdentities("key", http.Header{"Session-Id": []string{"session-b"}}, "grok", payload)
	if first.SessionAffinityKey == second.SessionAffinityKey {
		t.Fatal("different explicit sessions shared account affinity")
	}
	if first.PromptCacheKey != second.PromptCacheKey {
		t.Fatal("explicit sessions fragmented the same reusable static prefix")
	}
}
