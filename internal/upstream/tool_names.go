package upstream

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

func buildToolAliases(tools []any, operation Operation) map[string]string {
	aliases := make(map[string]string)
	used := make(map[string]struct{})
	for _, raw := range tools {
		tool, _ := raw.(map[string]any)
		if tool == nil {
			continue
		}
		if tool["type"] == "namespace" {
			for original, alias := range buildToolAliases(valueArray(tool["tools"]), operation) {
				aliases[original] = uniqueToolAlias(alias, used)
			}
			continue
		}
		name := ""
		if function, ok := tool["function"].(map[string]any); ok {
			name = stringValue(function, "name")
		} else if operation == OperationMessages || tool["type"] == "function" || tool["type"] == "custom" {
			name = stringValue(tool, "name")
		}
		if name == "" {
			continue
		}
		aliases[name] = uniqueToolAlias(preferredToolAlias(name), used)
	}
	return aliases
}

func preferredToolAlias(name string) string {
	if utf8.RuneCountInString(name) <= 64 {
		return name
	}
	if strings.HasPrefix(name, "mcp__") {
		if index := strings.LastIndex(name[5:], "__"); index >= 0 {
			return truncateRunes("mcp__"+name[5+index+2:], 64)
		}
	}
	return truncateRunes(name, 64)
}

func uniqueToolAlias(base string, used map[string]struct{}) string {
	candidate := base
	for suffix := 1; ; suffix++ {
		if _, exists := used[candidate]; !exists {
			used[candidate] = struct{}{}
			return candidate
		}
		marker := fmt.Sprintf("_%d", suffix)
		candidate = truncateRunes(base, 64-utf8.RuneCountInString(marker)) + marker
	}
}

func truncateRunes(value string, maximum int) string {
	if maximum <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum])
}

func applyToolAliases(payload map[string]any, aliases map[string]string) {
	if len(aliases) == 0 {
		return
	}
	for _, raw := range valueArray(payload["tools"]) {
		tool, _ := raw.(map[string]any)
		if tool == nil || tool["type"] != "function" {
			continue
		}
		if alias := aliases[stringValue(tool, "name")]; alias != "" {
			tool["name"] = alias
		}
	}
	for _, raw := range valueArray(payload["input"]) {
		item, _ := raw.(map[string]any)
		if item == nil || item["type"] != "function_call" {
			continue
		}
		if alias := aliases[stringValue(item, "name")]; alias != "" {
			item["name"] = alias
		}
	}
	if choice, ok := payload["tool_choice"].(map[string]any); ok {
		if alias := aliases[stringValue(choice, "name")]; alias != "" {
			choice["name"] = alias
		}
	}
}

func requestToolReverseNames(request Request) map[string]string {
	if request.CredentialKind == "" || len(request.Body) == 0 {
		return nil
	}
	var source map[string]any
	decoder := json.NewDecoder(bytes.NewReader(request.Body))
	decoder.UseNumber()
	if decoder.Decode(&source) != nil {
		return nil
	}
	aliases := buildToolAliases(valueArray(source["tools"]), request.Operation)
	if len(aliases) == 0 {
		return nil
	}
	reverse := make(map[string]string, len(aliases))
	for original, alias := range aliases {
		reverse[alias] = original
	}
	return reverse
}
