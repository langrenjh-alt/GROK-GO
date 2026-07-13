package gateway

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/accounts"
	"github.com/langrenjh-alt/GROK-GO/internal/upstream"
)

type accumulated struct {
	text      strings.Builder
	reasoning strings.Builder
	tools     []toolCall
	images    []string
	imageB64  []string
	videos    []string
	usage     upstream.Usage
	usageSeen bool
	err       error
}

type toolCall struct {
	ID        string
	Name      string
	Arguments string
	Index     int
}

func (a *accumulated) add(event upstream.Event) {
	switch event.Kind {
	case upstream.EventTextDelta:
		a.text.WriteString(event.Text)
	case upstream.EventReasoningDelta:
		a.reasoning.WriteString(event.Text)
	case upstream.EventToolCall:
		a.addTool(event)
	case upstream.EventImage:
		if event.URL != "" {
			a.images = appendUnique(a.images, event.URL)
		}
	case upstream.EventVideo:
		if event.URL != "" {
			a.videos = appendUnique(a.videos, event.URL)
		}
	case upstream.EventUsage:
		a.usage = event.Usage
		a.usageSeen = true
	case upstream.EventError:
		a.err = fmt.Errorf("%s", event.Error)
	}
}

func (a *accumulated) addTool(event upstream.Event) {
	id := event.ID
	if id == "" {
		id = fmt.Sprintf("call_%d", max(0, event.Index))
	}
	for index := range a.tools {
		if a.tools[index].ID == id || (event.ID == "" && a.tools[index].Index == event.Index) {
			if event.Name != "" {
				a.tools[index].Name = event.Name
			}
			a.tools[index].Arguments += event.Arguments
			return
		}
	}
	a.tools = append(a.tools, toolCall{ID: id, Name: event.Name, Arguments: event.Arguments, Index: event.Index})
}

func (h *Handler) nonStream(w http.ResponseWriter, r *http.Request, operation upstream.Operation, model string, requestPayload map[string]any, response *upstream.Response, lease *accounts.Lease) {
	if !isConversational(operation) && json.Valid(response.Body) && len(response.Body) > 0 {
		localizedBody, err := h.localizeBody(r.Context(), operation, response.Body)
		if err != nil {
			h.reportCompletion(r.Context(), lease, upstream.Usage{}, false)
			_ = lease.Release(context.Background(), accounts.Feedback{StatusCode: http.StatusBadGateway, Err: err})
			writeOperationError(w, operation, http.StatusBadGateway, err.Error(), "server_error", "media_cache_error", "")
			return
		}
		videoPolling := operation == upstream.OperationVideo && h.saveVideoAndStartPolling(r.Context(), localizedBody, lease)
		h.reportCompletion(r.Context(), lease, upstream.Usage{}, false)
		if !videoPolling {
			if operation == upstream.OperationVideo {
				saveVideoPayload(r.Context(), h.config.Videos, localizedBody)
			}
			_ = lease.Release(context.Background(), feedbackFromResponse(response, nil))
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(localizedBody)
		return
	}
	state := accumulated{}
	responseFormat, _ := requestPayload["response_format"].(string)
	responseFormat = strings.ToLower(strings.TrimSpace(responseFormat))
	if response.Events != nil {
		for event := range response.Events {
			if (operation == upstream.OperationImage || operation == upstream.OperationImageEdit) && responseFormat == "b64_json" && event.Kind == upstream.EventImage {
				encoded, err := h.localizeImageBase64(r.Context(), event.URL)
				if err != nil {
					state.add(upstream.Event{Kind: upstream.EventError, Error: fmt.Sprintf("cache upstream image: %v", err)})
					continue
				}
				state.imageB64 = append(state.imageB64, encoded)
				continue
			}
			state.add(h.localizeEvent(r.Context(), event))
		}
	}
	h.reportCompletion(r.Context(), lease, state.usage, state.usageSeen)
	if state.err != nil {
		_ = lease.Release(context.Background(), accounts.Feedback{StatusCode: http.StatusBadGateway, Err: state.err})
		writeOperationError(w, operation, http.StatusBadGateway, state.err.Error(), "server_error", "upstream_stream_error", "")
		return
	}
	_ = lease.Release(context.Background(), feedbackFromResponse(response, nil))
	switch operation {
	case upstream.OperationChat:
		writeJSON(w, http.StatusOK, chatResponse(model, state))
	case upstream.OperationResponses:
		writeJSON(w, http.StatusOK, responsesResponse(model, state))
	case upstream.OperationMessages:
		writeJSON(w, http.StatusOK, messagesResponse(model, state))
	case upstream.OperationImage, upstream.OperationImageEdit:
		data := make([]map[string]any, 0, max(len(state.images), len(state.imageB64)))
		if responseFormat == "b64_json" {
			for _, encoded := range state.imageB64 {
				data = append(data, map[string]any{"b64_json": encoded})
			}
		} else {
			for _, imageURL := range state.images {
				data = append(data, map[string]any{"url": imageURL})
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"created": time.Now().Unix(), "data": data})
	case upstream.OperationVideo:
		videoURL := ""
		if len(state.videos) > 0 {
			videoURL = state.videos[0]
		}
		payload, _ := json.Marshal(map[string]any{"id": newID("video"), "object": "video", "status": "completed", "url": videoURL, "created_at": time.Now().Unix()})
		saveVideoPayload(r.Context(), h.config.Videos, payload)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}
}

func imageDataPayload(value string) (string, bool) {
	if !strings.HasPrefix(strings.ToLower(value), "data:image/") {
		return "", false
	}
	header, encoded, ok := strings.Cut(value, ",")
	if !ok || !strings.HasSuffix(strings.ToLower(header), ";base64") || encoded == "" {
		return "", false
	}
	if _, err := base64.StdEncoding.DecodeString(encoded); err != nil {
		return "", false
	}
	return encoded, true
}

func chatResponse(model string, state accumulated) map[string]any {
	content := state.text.String() + mediaMarkdown(state)
	message := map[string]any{"role": "assistant", "content": content}
	if state.reasoning.Len() > 0 {
		message["reasoning_content"] = state.reasoning.String()
	}
	finishReason := "stop"
	if len(state.tools) > 0 {
		finishReason = "tool_calls"
		message["tool_calls"] = openAITools(state.tools)
	}
	return map[string]any{
		"id": newID("chatcmpl"), "object": "chat.completion", "created": time.Now().Unix(), "model": model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finishReason}},
		"usage":   chatUsage(state.usage),
	}
}

func responsesResponse(model string, state accumulated) map[string]any {
	var output []any
	if state.reasoning.Len() > 0 {
		output = append(output, map[string]any{"id": newID("rs"), "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": state.reasoning.String()}}})
	}
	if state.text.Len() > 0 || len(state.images)+len(state.videos) > 0 {
		output = append(output, map[string]any{"id": newID("msg"), "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": state.text.String() + mediaMarkdown(state), "annotations": []any{}}}})
	}
	for _, tool := range state.tools {
		output = append(output, map[string]any{"id": newID("fc"), "type": "function_call", "call_id": tool.ID, "name": tool.Name, "arguments": defaultArguments(tool.Arguments), "status": "completed"})
	}
	return map[string]any{"id": newID("resp"), "object": "response", "created_at": time.Now().Unix(), "status": "completed", "model": model, "output": output, "usage": responsesUsage(state.usage)}
}

func messagesResponse(model string, state accumulated) map[string]any {
	var content []any
	if state.reasoning.Len() > 0 {
		content = append(content, map[string]any{"type": "thinking", "thinking": state.reasoning.String()})
	}
	if state.text.Len() > 0 || len(state.images)+len(state.videos) > 0 {
		content = append(content, map[string]any{"type": "text", "text": state.text.String() + mediaMarkdown(state)})
	}
	for _, tool := range state.tools {
		var input any = map[string]any{}
		_ = json.Unmarshal([]byte(defaultArguments(tool.Arguments)), &input)
		content = append(content, map[string]any{"type": "tool_use", "id": tool.ID, "name": tool.Name, "input": input})
	}
	stopReason := "end_turn"
	if len(state.tools) > 0 {
		stopReason = "tool_use"
	}
	return map[string]any{"id": newID("msg"), "type": "message", "role": "assistant", "model": model, "content": content, "stop_reason": stopReason, "stop_sequence": nil, "usage": map[string]any{"input_tokens": state.usage.InputTokens, "output_tokens": state.usage.OutputTokens, "cache_read_input_tokens": state.usage.CachedTokens}}
}

func openAITools(tools []toolCall) []any {
	result := make([]any, 0, len(tools))
	for _, tool := range tools {
		result = append(result, map[string]any{"id": tool.ID, "type": "function", "function": map[string]any{"name": tool.Name, "arguments": defaultArguments(tool.Arguments)}})
	}
	return result
}

func defaultArguments(value string) string {
	if strings.TrimSpace(value) == "" {
		return "{}"
	}
	return value
}

func chatUsage(usage upstream.Usage) map[string]any {
	return map[string]any{"prompt_tokens": usage.InputTokens, "completion_tokens": usage.OutputTokens, "total_tokens": usage.InputTokens + usage.OutputTokens, "prompt_tokens_details": map[string]any{"cached_tokens": usage.CachedTokens}}
}

func responsesUsage(usage upstream.Usage) map[string]any {
	return map[string]any{"input_tokens": usage.InputTokens, "output_tokens": usage.OutputTokens, "total_tokens": usage.InputTokens + usage.OutputTokens, "input_tokens_details": map[string]any{"cached_tokens": usage.CachedTokens}}
}

func mediaMarkdown(state accumulated) string {
	var value strings.Builder
	for _, imageURL := range state.images {
		value.WriteString("\n\n![generated image](")
		value.WriteString(imageURL)
		value.WriteString(")")
	}
	for _, videoURL := range state.videos {
		value.WriteString("\n\n")
		value.WriteString(videoURL)
	}
	return value.String()
}

func appendUnique(values []string, value string) []string {
	for _, current := range values {
		if current == value {
			return values
		}
	}
	return append(values, value)
}

func newID(prefix string) string {
	var value [12]byte
	_, _ = rand.Read(value[:])
	return prefix + "_" + hex.EncodeToString(value[:])
}
