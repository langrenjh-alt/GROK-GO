package upstream

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

func prepareResponsesPayload(request Request, source map[string]any, model string) map[string]any {
	toolAliases := buildToolAliases(valueArray(source["tools"]), request.Operation)
	result := map[string]any{
		"model":               model,
		"stream":              request.Stream,
		"store":               false,
		"parallel_tool_calls": boolValue(source, "parallel_tool_calls", true),
	}

	switch request.Operation {
	case OperationResponses:
		for key, value := range source {
			result[key] = value
		}
		result["input"] = normalizeResponsesInput(source["input"])
		result["model"] = model
		result["stream"] = request.Stream
		result["store"] = false
	case OperationChat:
		result["input"] = normalizeMessages(valueArray(source["messages"]))
		copyRequestOptions(result, source)
	case OperationMessages:
		result["input"] = normalizeMessages(valueArray(source["messages"]))
		if instructions := systemText(source["system"]); instructions != "" {
			result["instructions"] = instructions
		}
		copyRequestOptions(result, source)
		if maximum, ok := source["max_tokens"]; ok {
			result["max_output_tokens"] = maximum
		}
	}

	tools, toolsPresent := source["tools"]
	normalizedTools := normalizeTools(valueArray(tools), request.Operation)
	if len(normalizedTools) > 0 {
		result["tools"] = normalizedTools
	} else {
		delete(result, "tools")
	}
	if choice, ok := source["tool_choice"]; ok {
		if normalized := normalizeToolChoice(choice, normalizedTools); normalized != nil {
			result["tool_choice"] = normalized
		} else {
			delete(result, "tool_choice")
		}
	}

	ensureReasoning(result, source, request)
	ensureInclude(result)
	if request.CredentialKind == domain.CredentialConsoleSSO {
		applyConsoleDefaults(result, source, request, model, toolsPresent)
	}
	if request.CredentialKind == domain.CredentialCLIOAuth && strings.TrimSpace(request.PromptCacheKey) != "" {
		result["prompt_cache_key"] = strings.TrimSpace(request.PromptCacheKey)
		_, choicePresent := source["tool_choice"]
		if !toolsPresent && !choicePresent && len(normalizedTools) == 0 {
			result["tools"] = []any{map[string]any{"type": "web_search"}, map[string]any{"type": "x_search"}}
			result["tool_choice"] = "none"
		}
	}
	applyToolAliases(result, toolAliases)
	return result
}

func prepareGrokWebPayload(request Request, source map[string]any, model string) map[string]any {
	if request.Operation == OperationVideo {
		result := map[string]any{"mediaType": "MEDIA_POST_TYPE_VIDEO"}
		if prompt := extractPrompt(request.Operation, source); prompt != "" {
			result["prompt"] = prompt
		}
		for _, key := range []string{"mediaUrl", "media_url", "image", "image_url", "aspect_ratio", "duration"} {
			if value, ok := source[key]; ok {
				result[key] = value
			}
		}
		return result
	}
	if request.Operation == OperationImageEdit {
		references := imageReferences(source)
		return map[string]any{
			"temporary":             true,
			"modelName":             "imagine-image-edit",
			"message":               extractPrompt(request.Operation, source),
			"enableImageGeneration": true,
			"returnImageBytes":      false,
			"enableImageStreaming":  true,
			"imageGenerationCount":  numberOr(source["n"], 2),
			"forceConcise":          false,
			"enableSideBySide":      true,
			"sendFinalMetadata":     true,
			"isReasoning":           false,
			"disableTextFollowUps":  true,
			"disableMemory":         true,
			"forceSideBySide":       false,
			"responseMetadata": map[string]any{"modelConfigOverride": map[string]any{"modelMap": map[string]any{
				"imageEditModel":       "imagine",
				"imageEditModelConfig": map[string]any{"imageReferences": references, "parentPostId": stringValue(source, "parent_post_id", "parentPostId")},
			}}},
		}
	}

	result := map[string]any{
		"collectionIds":               []any{},
		"connectors":                  []any{},
		"deviceEnvInfo":               map[string]any{"darkModeEnabled": false, "devicePixelRatio": 2, "screenHeight": 1329, "screenWidth": 2056, "viewportHeight": 1083, "viewportWidth": 2056},
		"disableMemory":               true,
		"disableSearch":               false,
		"disableSelfHarmShortCircuit": false,
		"disableTextFollowUps":        false,
		"enableImageGeneration":       true,
		"enableImageStreaming":        true,
		"enableSideBySide":            true,
		"fileAttachments":             []any{},
		"forceConcise":                false,
		"forceSideBySide":             false,
		"imageAttachments":            imageReferences(source),
		"imageGenerationCount":        numberOr(source["n"], 2),
		"isAsyncChat":                 false,
		"message":                     extractPrompt(request.Operation, source),
		"modeId":                      grokMode(model),
		"responseMetadata":            map[string]any{},
		"returnImageBytes":            false,
		"returnRawGrokInXaiRequest":   false,
		"searchAllConnectors":         false,
		"sendFinalMetadata":           true,
		"temporary":                   true,
		"toolOverrides": map[string]any{
			"gmailSearch": false, "googleCalendarSearch": false, "outlookSearch": false,
			"outlookCalendarSearch": false, "googleDriveSearch": false,
		},
	}
	return result
}

func normalizeMessages(messages []any) []any {
	result := make([]any, 0, len(messages))
	for _, raw := range messages {
		message, _ := raw.(map[string]any)
		if message == nil {
			continue
		}
		role := strings.ToLower(strings.TrimSpace(stringValue(message, "role")))
		if role == "tool" {
			result = append(result, map[string]any{
				"type": "function_call_output", "call_id": stringValue(message, "tool_call_id", "call_id"),
				"output": normalizeToolOutput(message["content"]),
			})
			continue
		}

		parts, sideItems := normalizeContent(role, message["content"])
		if len(parts) > 0 || (role != "assistant" && len(sideItems) == 0) {
			mappedRole := role
			if mappedRole == "system" {
				mappedRole = "developer"
			}
			if mappedRole == "" {
				mappedRole = "user"
			}
			result = append(result, map[string]any{"type": "message", "role": mappedRole, "content": parts})
		}
		result = append(result, sideItems...)

		if role == "assistant" {
			for _, rawCall := range valueArray(message["tool_calls"]) {
				call, _ := rawCall.(map[string]any)
				function, _ := call["function"].(map[string]any)
				if call == nil || function == nil || stringValue(call, "type") != "function" {
					continue
				}
				result = append(result, map[string]any{
					"type": "function_call", "call_id": stringValue(call, "id", "call_id"),
					"name": stringValue(function, "name"), "arguments": stringValue(function, "arguments"),
				})
			}
		}
	}
	return result
}

func normalizeContent(role string, content any) ([]any, []any) {
	partType := "input_text"
	if role == "assistant" {
		partType = "output_text"
	}
	if text, ok := content.(string); ok {
		if text == "" {
			return []any{}, nil
		}
		return []any{map[string]any{"type": partType, "text": text}}, nil
	}
	parts := make([]any, 0)
	sideItems := make([]any, 0)
	for _, raw := range valueArray(content) {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		typeName := strings.ToLower(stringValue(item, "type"))
		switch typeName {
		case "text", "input_text", "output_text", "thinking":
			text := stringValue(item, "text", "thinking")
			if text != "" {
				parts = append(parts, map[string]any{"type": partType, "text": text})
			}
		case "image_url", "input_image", "image":
			if role == "assistant" {
				continue
			}
			if image := normalizedImage(item); image != nil {
				parts = append(parts, image)
			}
		case "file", "input_file":
			if role == "assistant" {
				continue
			}
			if file := normalizedFile(item); file != nil {
				parts = append(parts, file)
			}
		case "input_audio":
			if role != "assistant" {
				if audio := normalizedAudio(item); audio != nil {
					parts = append(parts, audio)
				}
			}
		case "tool_use":
			arguments, _ := json.Marshal(item["input"])
			sideItems = append(sideItems, map[string]any{"type": "function_call", "call_id": stringValue(item, "id"), "name": stringValue(item, "name"), "arguments": string(arguments)})
		case "tool_result":
			sideItems = append(sideItems, map[string]any{"type": "function_call_output", "call_id": stringValue(item, "tool_use_id", "call_id"), "output": normalizeToolOutput(item["content"])})
		}
	}
	return parts, sideItems
}

func normalizedImage(item map[string]any) map[string]any {
	value := item["image_url"]
	if source, ok := value.(map[string]any); ok {
		value = source["url"]
	}
	if stringValue(item, "type") == "image" {
		if source, ok := item["source"].(map[string]any); ok {
			if source["type"] == "base64" {
				value = "data:" + stringValue(source, "media_type") + ";base64," + stringValue(source, "data")
			} else {
				value = source["url"]
			}
		}
	}
	text, _ := value.(string)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return map[string]any{"type": "input_image", "image_url": text}
}

func normalizedFile(item map[string]any) map[string]any {
	source, _ := item["file"].(map[string]any)
	if source == nil {
		source = item
	}
	result := map[string]any{"type": "input_file"}
	for _, key := range []string{"file_id", "file_data", "file_url", "filename"} {
		if value, ok := source[key]; ok && value != nil && value != "" {
			result[key] = value
		}
	}
	if len(result) == 1 {
		return nil
	}
	return result
}

func normalizedAudio(item map[string]any) map[string]any {
	source, _ := item["input_audio"].(map[string]any)
	if source == nil || stringValue(source, "data") == "" {
		return nil
	}
	result := map[string]any{"type": "input_audio", "data": source["data"]}
	if format := stringValue(source, "format"); format != "" {
		result["format"] = format
	}
	return result
}

func normalizeToolOutput(content any) any {
	if text, ok := content.(string); ok {
		return text
	}
	if items := valueArray(content); items != nil {
		result := make([]any, 0, len(items))
		for _, raw := range items {
			item, _ := raw.(map[string]any)
			if item == nil {
				encoded, _ := json.Marshal(raw)
				result = append(result, map[string]any{"type": "input_text", "text": string(encoded)})
				continue
			}
			switch stringValue(item, "type") {
			case "text":
				result = append(result, map[string]any{"type": "input_text", "text": stringValue(item, "text")})
			case "image", "image_url", "input_image":
				if image := normalizedImage(item); image != nil {
					result = append(result, image)
				}
			case "file", "input_file":
				if file := normalizedFile(item); file != nil {
					result = append(result, file)
				}
			default:
				encoded, _ := json.Marshal(item)
				result = append(result, map[string]any{"type": "input_text", "text": string(encoded)})
			}
		}
		return result
	}
	encoded, _ := json.Marshal(content)
	return string(encoded)
}

func normalizeResponsesInput(input any) []any {
	if text, ok := input.(string); ok {
		return []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}}}
	}
	result := make([]any, 0)
	for _, raw := range valueArray(input) {
		item, _ := raw.(map[string]any)
		if item == nil {
			continue
		}
		copy := cloneMapValue(item)
		if stringValue(copy, "role") == "system" {
			copy["role"] = "developer"
		}
		result = append(result, copy)
	}
	return result
}

func normalizeTools(tools []any, operation Operation) []any {
	flattened := make([]any, 0, len(tools))
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		if tool == nil {
			continue
		}
		if tool["type"] == "namespace" {
			flattened = append(flattened, valueArray(tool["tools"])...)
		} else {
			flattened = append(flattened, tool)
		}
	}
	result := make([]any, 0, len(flattened))
	for _, raw := range flattened {
		tool, _ := raw.(map[string]any)
		if tool == nil {
			continue
		}
		copy := cloneMapValue(tool)
		typeName := stringValue(copy, "type")
		if function, ok := copy["function"].(map[string]any); ok && typeName == "function" {
			copy = map[string]any{"type": "function", "name": stringValue(function, "name")}
			for _, key := range []string{"description", "parameters", "strict"} {
				if value, exists := function[key]; exists {
					copy[key] = value
				}
			}
		} else if operation == OperationMessages && typeName == "" && stringValue(copy, "name") != "" {
			copy["type"] = "function"
			if schema, ok := copy["input_schema"]; ok {
				copy["parameters"] = schema
				delete(copy, "input_schema")
			}
		}
		typeName = stringValue(copy, "type")
		if typeName == "tool_search" || typeName == "image_generation" || (typeName == "custom" && stringValue(copy, "name") == "apply_patch") {
			continue
		}
		if typeName == "custom" {
			copy["type"] = "function"
			typeName = "function"
		}
		if typeName == "web_search_preview" || typeName == "web_search_preview_2025_03_11" {
			copy["type"] = "web_search"
			typeName = "web_search"
		}
		if typeName == "web_search" {
			delete(copy, "external_web_access")
		}
		if typeName == "function" {
			if _, ok := copy["parameters"].(map[string]any); !ok {
				copy["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
			}
		}
		result = append(result, copy)
	}
	return result
}

func normalizeToolChoice(choice any, tools []any) any {
	value, ok := choice.(map[string]any)
	if !ok {
		return choice
	}
	copy := cloneMapValue(value)
	typeName := stringValue(copy, "type")
	if typeName == "tool" {
		return map[string]any{"type": "function", "name": stringValue(copy, "name")}
	}
	if typeName == "function" || typeName == "custom" {
		name := stringValue(copy, "name")
		if nested, ok := copy["function"].(map[string]any); ok && name == "" {
			name = stringValue(nested, "name")
		}
		if name == "" || !containsFunction(tools, name) {
			return nil
		}
		return map[string]any{"type": "function", "name": name}
	}
	return copy
}

func containsFunction(tools []any, name string) bool {
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		if tool != nil && tool["type"] == "function" && stringValue(tool, "name") == name {
			return true
		}
	}
	return false
}

func copyRequestOptions(result, source map[string]any) {
	for _, key := range []string{"temperature", "top_p", "metadata", "max_output_tokens", "service_tier", "user"} {
		if value, ok := source[key]; ok {
			result[key] = value
		}
	}
	if maximum, ok := source["max_tokens"]; ok {
		result["max_output_tokens"] = maximum
	}
	if instructions, ok := source["instructions"]; ok {
		result["instructions"] = instructions
	}
}

func ensureReasoning(result, source map[string]any, request Request) {
	if reasoning, ok := source["reasoning"].(map[string]any); ok {
		result["reasoning"] = reasoning
		return
	}
	effort := strings.TrimSpace(stringValue(source, "reasoning_effort"))
	if effort == "" && request.Operation == OperationMessages {
		if thinking, ok := source["thinking"].(map[string]any); ok && thinking["type"] == "enabled" {
			effort = "medium"
		}
	}
	if effort == "" {
		effort = "medium"
	}
	result["reasoning"] = map[string]any{"effort": normalizeEffort(effort), "summary": "auto"}
}

func ensureInclude(result map[string]any) {
	values := make([]any, 0)
	seen := false
	for _, raw := range valueArray(result["include"]) {
		values = append(values, raw)
		if raw == "reasoning.encrypted_content" {
			seen = true
		}
	}
	if !seen {
		values = append(values, "reasoning.encrypted_content")
	}
	result["include"] = values
}

func applyConsoleDefaults(result, source map[string]any, request Request, model string, toolsPresent bool) {
	maximum := 1_000_000
	lower := strings.ToLower(request.Model + " " + model)
	if strings.Contains(lower, "multi-agent") {
		maximum = 2_000_000
	} else if strings.Contains(lower, "grok-build") {
		maximum = 256_000
	}
	if _, ok := source["max_output_tokens"]; !ok {
		result["max_output_tokens"] = maximum
	}
	if fixed := fixedConsoleEffort(request.Model); fixed != "" {
		result["reasoning"] = map[string]any{"effort": fixed, "summary": "auto"}
	}
	if strings.Contains(strings.ToLower(model), "grok-4.20") && !strings.Contains(strings.ToLower(model), "multi-agent") {
		delete(result, "reasoning")
	}
	if !toolsPresent && consoleSearchModel(model) {
		result["tools"] = []any{
			map[string]any{"type": "web_search", "enable_image_understanding": true},
			map[string]any{"type": "x_search", "enable_video_understanding": true},
		}
		result["tool_choice"] = "auto"
	}
}

func fixedConsoleEffort(model string) string {
	lower := strings.ToLower(model)
	for _, effort := range []string{"xhigh", "medium", "high", "low"} {
		if strings.HasSuffix(lower, "-"+effort) {
			return effort
		}
	}
	return ""
}

func consoleSearchModel(model string) bool {
	lower := strings.ToLower(model)
	return strings.Contains(lower, "grok-4.3") || strings.Contains(lower, "grok-4.20") || strings.Contains(lower, "grok-build")
}

func imageReferences(source map[string]any) []any {
	result := make([]any, 0)
	for _, key := range []string{"image", "images", "image_url", "image_urls", "image_references"} {
		value, ok := source[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			if strings.TrimSpace(typed) != "" {
				result = append(result, typed)
			}
		case []any:
			for _, raw := range typed {
				switch item := raw.(type) {
				case string:
					if strings.TrimSpace(item) != "" {
						result = append(result, item)
					}
				case map[string]any:
					if data := stringValue(item, "data", "url", "image_url"); data != "" {
						result = append(result, data)
					}
				}
			}
		}
	}
	return result
}

func normalizeEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none", "low", "medium", "high", "xhigh":
		return strings.ToLower(strings.TrimSpace(value))
	case "minimal":
		return "low"
	default:
		return "medium"
	}
}

func boolValue(source map[string]any, key string, fallback bool) bool {
	value, ok := source[key].(bool)
	if !ok {
		return fallback
	}
	return value
}

func numberOr(value any, fallback int) any {
	if value == nil || strings.TrimSpace(fmt.Sprint(value)) == "" {
		return fallback
	}
	if parsed := intValue(value); parsed > 0 {
		return parsed
	}
	return value
}

func valueArray(value any) []any {
	items, _ := value.([]any)
	return items
}

func cloneMapValue(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
