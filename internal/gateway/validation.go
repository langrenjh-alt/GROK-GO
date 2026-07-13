package gateway

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/langrenjh-alt/GROK-GO/internal/upstream"
)

type validationIssue struct {
	message string
	code    string
	param   string
}

func validateOperationPayload(operation upstream.Operation, payload map[string]any) *validationIssue {
	switch operation {
	case upstream.OperationResponses:
		if emptyInput(payload["input"]) {
			return invalidParameter("input", "input cannot be empty")
		}
	case upstream.OperationImage:
		if strings.TrimSpace(textValue(payload["prompt"])) == "" {
			return invalidParameter("prompt", "prompt cannot be empty")
		}
		if issue := validateCount(payload["n"], 4, "n"); issue != nil {
			return issue
		}
		if issue := validateResponseFormat(payload["response_format"]); issue != nil {
			return issue
		}
	case upstream.OperationImageEdit:
		if strings.TrimSpace(textValue(payload["prompt"])) == "" {
			return invalidParameter("prompt", "prompt cannot be empty")
		}
		if !hasValues(payload["image"]) && !hasValues(payload["images"]) {
			return invalidParameter("image", "at least one image is required")
		}
		if hasValues(payload["mask"]) {
			return invalidParameter("mask", "mask is not supported")
		}
		if issue := validateCount(payload["n"], 2, "n"); issue != nil {
			return issue
		}
		if size := strings.TrimSpace(textValue(payload["size"])); size != "" && size != "1024x1024" {
			return invalidParameter("size", "image edit currently supports only 1024x1024")
		}
		if issue := validateResponseFormat(payload["response_format"]); issue != nil {
			return issue
		}
	case upstream.OperationVideo:
		if strings.TrimSpace(textValue(payload["prompt"])) == "" {
			return invalidParameter("prompt", "prompt cannot be empty")
		}
		if hasScalar(payload["seconds"]) {
			seconds, valid := integerValue(payload["seconds"])
			if !valid || seconds != 6 && seconds != 10 && seconds != 12 && seconds != 16 && seconds != 20 {
				return invalidParameter("seconds", "seconds must be one of 6, 10, 12, 16, or 20")
			}
		}
		if size := strings.TrimSpace(textValue(payload["size"])); size != "" && !containsString([]string{"720x1280", "1280x720", "1024x1024", "1024x1792", "1792x1024"}, size) {
			return invalidParameter("size", "size is not supported")
		}
		if resolution := strings.ToLower(strings.TrimSpace(textValue(payload["resolution_name"]))); resolution != "" && resolution != "480p" && resolution != "720p" {
			return invalidParameter("resolution_name", "resolution_name must be 480p or 720p")
		}
		if preset := strings.ToLower(strings.TrimSpace(textValue(payload["preset"]))); preset != "" && !containsString([]string{"fun", "normal", "spicy", "custom"}, preset) {
			return invalidParameter("preset", "preset must be fun, normal, spicy, or custom")
		}
	}
	return nil
}

func validateCount(value any, maximum int, param string) *validationIssue {
	if !hasScalar(value) {
		return nil
	}
	count, valid := integerValue(value)
	if !valid || count < 1 || count > maximum {
		return invalidParameter(param, fmt.Sprintf("%s must be between 1 and %d", param, maximum))
	}
	return nil
}

func hasScalar(value any) bool {
	return value != nil && strings.TrimSpace(fmt.Sprint(value)) != ""
}

func validateResponseFormat(value any) *validationIssue {
	format := strings.ToLower(strings.TrimSpace(textValue(value)))
	if format != "" && format != "url" && format != "b64_json" {
		return invalidParameter("response_format", "response_format must be url or b64_json")
	}
	return nil
}

func integerValue(value any) (int, bool) {
	if value == nil || strings.TrimSpace(fmt.Sprint(value)) == "" {
		return 0, false
	}
	switch value := value.(type) {
	case json.Number:
		parsed, err := value.Int64()
		return int(parsed), err == nil && int64(int(parsed)) == parsed
	case float64:
		return int(value), value == float64(int(value))
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		return parsed, err == nil
	case int:
		return value, true
	case int64:
		return int(value), int64(int(value)) == value
	default:
		return 0, false
	}
}

func emptyInput(value any) bool {
	switch value := value.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(value) == ""
	case []any:
		return len(value) == 0
	default:
		return false
	}
}

func hasValues(value any) bool {
	switch value := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(value) != ""
	case []any:
		return len(value) > 0
	default:
		return true
	}
}

func textValue(value any) string {
	text, _ := value.(string)
	return text
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func invalidParameter(param, message string) *validationIssue {
	return &validationIssue{message: message, code: "invalid_value", param: param}
}
