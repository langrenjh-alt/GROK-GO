package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/langrenjh-alt/GROK-GO/internal/apikey"
)

var claudeSessionSuffix = regexp.MustCompile(`(?i)_session_([a-f0-9-]+)$`)

type cacheIdentities struct {
	SessionAffinityKey string
	PromptCacheKey     string
}

func requestCacheIdentities(r *http.Request, model string, payload map[string]any) cacheIdentities {
	tenantID := ""
	if key, ok := apikey.FromContext(r.Context()); ok {
		tenantID = key.ID
	}
	return resolveCacheIdentities(tenantID, r.Header, model, payload)
}

// resolveCacheIdentities keeps conversation affinity separate from the
// reusable static prefix sent to the upstream prompt cache.
func resolveCacheIdentities(tenantID string, headers http.Header, model string, payload map[string]any) cacheIdentities {
	return cacheIdentities{
		SessionAffinityKey: uuidCacheIdentity("grok-session-affinity:v2", tenantID, model, sessionAffinitySeed(headers, model, payload)),
		PromptCacheKey:     uuidCacheIdentity("grok-prompt-cache:v2", tenantID, model, promptCachePrefix(model, payload)),
	}
}

func sessionAffinitySeed(headers http.Header, model string, payload map[string]any) string {
	seed := requestSession(headers)
	if seed == "" {
		seed, _ = payload["prompt_cache_key"].(string)
		seed = strings.TrimSpace(seed)
	}
	if seed == "" {
		seed = metadataSession(payload["metadata"])
	}
	if seed == "" {
		seed = anthropicCacheAnchor(payload["system"], valueSlice(payload["messages"]))
	}
	if seed == "" {
		seed = cachePrefix(model, payload)
	}
	return seed
}

func uuidCacheIdentity(namespace, tenantID, model, seed string) string {
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		tenantID = "public"
	} else {
		digest := sha256.Sum256([]byte("grok-go:client-key:" + tenantID))
		tenantID = hex.EncodeToString(digest[:12])
	}
	digest := sha256.Sum256([]byte(namespace + ":" + tenantID + ":" + strings.ToLower(strings.TrimSpace(model)) + ":" + seed))
	value := digest[:16]
	value[6] = (value[6] & 0x0f) | 0x50
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value)
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

func requestSession(headers http.Header) string {
	for _, name := range []string{
		"Session-Id", "X-Session-Id", "Conversation-Id", "X-Conversation-Id",
		"X-Grok-Conv-Id", "X-Claude-Code-Session-Id",
	} {
		if value := strings.TrimSpace(headers.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func metadataSession(value any) string {
	metadata, _ := value.(map[string]any)
	if metadata == nil {
		return ""
	}
	if value, _ := metadata["session_id"].(string); strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	userID, _ := metadata["user_id"].(string)
	userID = strings.TrimSpace(userID)
	if match := claudeSessionSuffix.FindStringSubmatch(userID); len(match) == 2 {
		return match[1]
	}
	if strings.HasPrefix(userID, "{") {
		var decoded map[string]any
		if json.Unmarshal([]byte(userID), &decoded) == nil {
			if value, _ := decoded["session_id"].(string); strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func anthropicCacheAnchor(system any, messages []any) string {
	parts := make([]string, 0)
	for _, text := range cachedTextBlocks(system) {
		parts = append(parts, "system:"+text)
	}
	firstUser := ""
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message == nil {
			continue
		}
		role, _ := message["role"].(string)
		cached := cachedTextBlocks(message["content"])
		if role == "user" && firstUser == "" && len(cached) > 0 {
			firstUser = cached[0]
		} else if role == "assistant" {
			for _, text := range cached {
				parts = append(parts, "assistant:"+text)
			}
		}
	}
	if firstUser != "" {
		parts = append(parts, "user_anchor:"+firstUser)
	}
	return strings.Join(parts, "\n")
}

func cachedTextBlocks(value any) []string {
	blocks, _ := value.([]any)
	result := make([]string, 0)
	for _, raw := range blocks {
		block, _ := raw.(map[string]any)
		control, _ := block["cache_control"].(map[string]any)
		if block == nil || block["type"] != "text" || control == nil || control["type"] != "ephemeral" {
			continue
		}
		if text, _ := block["text"].(string); strings.TrimSpace(text) != "" {
			result = append(result, strings.TrimSpace(text))
		}
	}
	return result
}

func promptCachePrefix(model string, payload map[string]any) string {
	parts := []string{"model=" + strings.ToLower(strings.TrimSpace(model))}
	staticPrefix := false
	for _, key := range []string{"instructions", "system", "developer"} {
		if encoded, ok := normalizedPromptContent(payload[key]); ok {
			parts = append(parts, "instruction="+encoded)
			staticPrefix = true
		}
	}
	for _, key := range []string{"messages", "input"} {
		for _, raw := range valueSlice(payload[key]) {
			message, _ := raw.(map[string]any)
			if message == nil {
				continue
			}
			role := strings.ToLower(strings.TrimSpace(stringField(message, "role")))
			if role != "system" && role != "developer" {
				continue
			}
			if encoded, ok := normalizedPromptContent(message["content"]); ok {
				parts = append(parts, "instruction="+encoded)
				staticPrefix = true
			}
		}
	}
	if tools, ok := payload["tools"]; ok && meaningfulCacheValue(tools) {
		parts = append(parts, "tools="+canonicalJSON(tools))
		staticPrefix = true
	}
	if !staticPrefix {
		appendFirstUserPrefix(&parts, payload)
	}
	return strings.Join(parts, "|")
}

func normalizedPromptContent(value any) (string, bool) {
	if !meaningfulCacheValue(value) {
		return "", false
	}
	if text, ok := value.(string); ok {
		return canonicalJSON(text), true
	}
	blocks, ok := value.([]any)
	if !ok {
		return canonicalJSON(value), true
	}
	texts := make([]string, 0, len(blocks))
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok {
			return canonicalJSON(value), true
		}
		typeName := strings.ToLower(strings.TrimSpace(stringField(block, "type")))
		if typeName != "text" && typeName != "input_text" && typeName != "output_text" {
			return canonicalJSON(value), true
		}
		text := stringField(block, "text")
		if strings.TrimSpace(text) == "" {
			continue
		}
		texts = append(texts, text)
	}
	if len(texts) == 0 {
		return "", false
	}
	return canonicalJSON(strings.Join(texts, "\n")), true
}

func meaningfulCacheValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

func appendFirstUserPrefix(parts *[]string, payload map[string]any) {
	if appendFirstUserFromItems(parts, valueSlice(payload["messages"])) {
		return
	}
	if text, ok := payload["input"].(string); ok {
		if strings.TrimSpace(text) != "" {
			*parts = append(*parts, "first_user="+canonicalJSON(text))
		}
		return
	}
	appendFirstUserFromItems(parts, valueSlice(payload["input"]))
}

func appendFirstUserFromItems(parts *[]string, items []any) bool {
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(stringField(item, "role")))
		if role == "user" {
			if encoded, ok := normalizedPromptContent(item["content"]); ok {
				*parts = append(*parts, "first_user="+encoded)
				return true
			}
		}
		if role == "" && strings.EqualFold(stringField(item, "type"), "input_text") && meaningfulCacheValue(item["text"]) {
			*parts = append(*parts, "first_user="+canonicalJSON(item["text"]))
			return true
		}
	}
	return false
}

func cachePrefix(model string, payload map[string]any) string {
	parts := []string{"model=" + strings.ToLower(strings.TrimSpace(model))}
	for _, key := range []string{"tools", "instructions", "system"} {
		if value, ok := payload[key]; ok && value != nil {
			parts = append(parts, key+"="+canonicalJSON(value))
		}
	}
	if messages := valueSlice(payload["messages"]); len(messages) > 0 {
		appendMessagePrefix(&parts, messages)
	} else if input, ok := payload["input"]; ok {
		appendResponsesPrefix(&parts, input)
	}
	return strings.Join(parts, "|")
}

func appendMessagePrefix(parts *[]string, messages []any) {
	firstUser := false
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message == nil {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(stringField(message, "role")))
		switch {
		case role == "system" || role == "developer":
			*parts = append(*parts, "system="+canonicalJSON(message["content"]))
		case role == "user" && !firstUser:
			*parts = append(*parts, "first_user="+canonicalJSON(message["content"]))
			firstUser = true
		}
	}
}

func appendResponsesPrefix(parts *[]string, input any) {
	if text, ok := input.(string); ok {
		*parts = append(*parts, "first_user="+canonicalJSON(text))
		return
	}
	items := valueSlice(input)
	firstUser := false
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(stringField(item, "role")))
		switch {
		case role == "system" || role == "developer":
			*parts = append(*parts, "system="+canonicalJSON(item["content"]))
		case role == "user" && !firstUser:
			*parts = append(*parts, "first_user="+canonicalJSON(item["content"]))
			firstUser = true
		case !firstUser && item["type"] == "input_text":
			*parts = append(*parts, "first_user="+canonicalJSON(item["text"]))
			firstUser = true
		}
	}
}

func canonicalJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(encoded)
}

func valueSlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func stringField(value map[string]any, key string) string {
	text, _ := value[key].(string)
	return text
}
