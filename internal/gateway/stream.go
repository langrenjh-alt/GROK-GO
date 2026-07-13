package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/accounts"
	"github.com/langrenjh-alt/GROK-GO/internal/httpx"
	"github.com/langrenjh-alt/GROK-GO/internal/upstream"
)

func (h *Handler) stream(w http.ResponseWriter, r *http.Request, operation upstream.Operation, model string, response *upstream.Response, lease *accounts.Lease) {
	// Server WriteTimeout is an absolute deadline. Streaming requests manage
	// their own upstream timeout and heartbeat, so clear the connection deadline.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	_, _ = fmt.Fprint(w, ": heartbeat\n\n")
	if flusher != nil {
		flusher.Flush()
	}

	state := &streamState{operation: operation, model: model, responseID: streamResponseID(operation)}
	state.start(w)
	if flusher != nil {
		flusher.Flush()
	}
	ticker := time.NewTicker(h.config.HeartbeatInterval)
	defer ticker.Stop()
	events := response.Events
	if events == nil {
		events = upstream.Events(upstream.Event{Kind: upstream.EventDone})
	}
	completed := false
	status := http.StatusOK
	var streamErr error
	for !completed {
		select {
		case <-r.Context().Done():
			streamErr = r.Context().Err()
			status = 499
			completed = true
		case <-ticker.C:
			_, _ = fmt.Fprint(w, ": heartbeat\n\n")
			if flusher != nil {
				flusher.Flush()
			}
		case event, ok := <-events:
			if !ok {
				state.finish(w)
				completed = true
				break
			}
			event = h.localizeEvent(r.Context(), event)
			if event.Kind == upstream.EventError {
				streamErr = fmt.Errorf("%s", event.Error)
				status = http.StatusBadGateway
				state.writeError(w, event.Error)
				completed = true
			} else if event.Kind == upstream.EventDone {
				state.finish(w)
				completed = true
			} else {
				state.event(w, event)
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
	feedback := feedbackFromResponse(response, streamErr)
	feedback.StatusCode = status
	if status >= http.StatusBadRequest {
		code := "upstream_stream_error"
		if status == 499 {
			code = "client_closed_request"
		}
		httpx.ReportOutcome(r.Context(), status, code)
	}
	h.reportCompletion(r.Context(), lease, state.usage, state.usageSeen)
	_ = lease.Release(context.Background(), feedback)
}

type streamState struct {
	operation  upstream.Operation
	model      string
	responseID string
	usage      upstream.Usage
	usageSeen  bool

	text      strings.Builder
	reasoning strings.Builder
	tools     []*streamToolCall
	toolByKey map[string]*streamToolCall

	blockOpen   bool
	blockKind   upstream.EventKind
	blockIndex  int
	blockToolID string

	responseNextOutput     int
	responseTextStarted    bool
	responseTextIndex      int
	responseMessageID      string
	responseReasoningStart bool
	responseReasoningIndex int
	responseReasoningID    string
}

type streamToolCall struct {
	id          string
	itemID      string
	name        string
	index       int
	outputIndex int
	arguments   strings.Builder
}

func (s *streamState) start(w http.ResponseWriter) {
	switch s.operation {
	case upstream.OperationChat:
		writeData(w, map[string]any{"id": s.responseID, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": s.model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{"role": "assistant"}, "finish_reason": nil}}})
	case upstream.OperationResponses:
		writeEvent(w, "response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": s.responseID, "object": "response", "status": "in_progress", "model": s.model, "output": []any{}}})
	case upstream.OperationMessages:
		writeEvent(w, "message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": s.responseID, "type": "message", "role": "assistant", "model": s.model, "content": []any{}, "stop_reason": nil, "usage": map[string]any{"input_tokens": 0, "output_tokens": 0}}})
		writeEvent(w, "ping", map[string]any{"type": "ping"})
	}
}

func (s *streamState) event(w http.ResponseWriter, event upstream.Event) {
	if event.Kind == upstream.EventUsage {
		s.usage = event.Usage
		s.usageSeen = true
		return
	}
	tool, fresh := s.record(event)
	switch s.operation {
	case upstream.OperationChat:
		s.chatEvent(w, event, tool, fresh)
	case upstream.OperationResponses:
		s.responsesEvent(w, event, tool, fresh)
	case upstream.OperationMessages:
		s.messagesEvent(w, event, tool, fresh)
	}
}

func (s *streamState) record(event upstream.Event) (*streamToolCall, bool) {
	switch event.Kind {
	case upstream.EventTextDelta:
		s.text.WriteString(event.Text)
	case upstream.EventReasoningDelta:
		s.reasoning.WriteString(event.Text)
	case upstream.EventImage:
		s.text.WriteString("\n\n![generated image](" + event.URL + ")")
	case upstream.EventVideo:
		s.text.WriteString("\n\n" + event.URL)
	case upstream.EventToolCall:
		if s.toolByKey == nil {
			s.toolByKey = make(map[string]*streamToolCall)
		}
		key := event.ID
		if key == "" {
			key = fmt.Sprintf("index:%d", event.Index)
		}
		tool := s.toolByKey[key]
		fresh := tool == nil
		if fresh {
			id := event.ID
			if id == "" {
				id = fmt.Sprintf("call_%d", len(s.tools))
			}
			itemID := event.ItemID
			if itemID == "" {
				itemID = newID("fc")
			}
			tool = &streamToolCall{id: id, itemID: itemID, name: event.Name, index: len(s.tools), outputIndex: -1}
			s.tools = append(s.tools, tool)
			s.toolByKey[key] = tool
			s.toolByKey[id] = tool
		}
		if event.ItemID != "" {
			tool.itemID = event.ItemID
			s.toolByKey[event.ItemID] = tool
		}
		if event.Name != "" {
			tool.name = event.Name
		}
		tool.arguments.WriteString(event.Arguments)
		return tool, fresh
	}
	return nil, false
}

func (s *streamState) chatEvent(w http.ResponseWriter, event upstream.Event, tool *streamToolCall, fresh bool) {
	delta := map[string]any{}
	switch event.Kind {
	case upstream.EventTextDelta:
		delta["content"] = event.Text
	case upstream.EventReasoningDelta:
		delta["reasoning_content"] = event.Text
	case upstream.EventToolCall:
		if tool == nil {
			return
		}
		call := map[string]any{"index": tool.index, "function": map[string]any{"arguments": event.Arguments}}
		if fresh {
			call["id"] = tool.id
			call["type"] = "function"
			call["function"].(map[string]any)["name"] = tool.name
		}
		delta["tool_calls"] = []any{call}
	case upstream.EventImage:
		delta["content"] = "\n\n![generated image](" + event.URL + ")"
	case upstream.EventVideo:
		delta["content"] = "\n\n" + event.URL
	default:
		return
	}
	writeData(w, map[string]any{"id": s.responseID, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": s.model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": nil}}})
}

func (s *streamState) responsesEvent(w http.ResponseWriter, event upstream.Event, tool *streamToolCall, fresh bool) {
	switch event.Kind {
	case upstream.EventTextDelta:
		s.ensureResponseText(w)
		writeEvent(w, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "response_id": s.responseID, "item_id": s.responseMessageID, "output_index": s.responseTextIndex, "content_index": 0, "delta": event.Text})
	case upstream.EventReasoningDelta:
		s.ensureResponseReasoning(w)
		writeEvent(w, "response.reasoning_summary_text.delta", map[string]any{"type": "response.reasoning_summary_text.delta", "response_id": s.responseID, "item_id": s.responseReasoningID, "output_index": s.responseReasoningIndex, "summary_index": 0, "delta": event.Text})
	case upstream.EventToolCall:
		if tool == nil {
			return
		}
		if fresh || tool.outputIndex < 0 {
			tool.outputIndex = s.responseNextOutput
			s.responseNextOutput++
			writeEvent(w, "response.output_item.added", map[string]any{"type": "response.output_item.added", "response_id": s.responseID, "output_index": tool.outputIndex, "item": map[string]any{"id": tool.itemID, "type": "function_call", "call_id": tool.id, "name": tool.name, "arguments": "", "status": "in_progress"}})
		}
		if event.Arguments != "" {
			writeEvent(w, "response.function_call_arguments.delta", map[string]any{"type": "response.function_call_arguments.delta", "response_id": s.responseID, "item_id": tool.itemID, "output_index": tool.outputIndex, "delta": event.Arguments})
		}
	case upstream.EventImage:
		s.ensureResponseText(w)
		writeEvent(w, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "response_id": s.responseID, "item_id": s.responseMessageID, "output_index": s.responseTextIndex, "content_index": 0, "delta": "\n\n![generated image](" + event.URL + ")"})
	case upstream.EventVideo:
		s.ensureResponseText(w)
		writeEvent(w, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "response_id": s.responseID, "item_id": s.responseMessageID, "output_index": s.responseTextIndex, "content_index": 0, "delta": "\n\n" + event.URL})
	}
}

func (s *streamState) ensureResponseText(w http.ResponseWriter) {
	if s.responseTextStarted {
		return
	}
	s.responseTextStarted = true
	s.responseTextIndex = s.responseNextOutput
	s.responseNextOutput++
	s.responseMessageID = newID("msg")
	writeEvent(w, "response.output_item.added", map[string]any{"type": "response.output_item.added", "response_id": s.responseID, "output_index": s.responseTextIndex, "item": map[string]any{"id": s.responseMessageID, "type": "message", "role": "assistant", "status": "in_progress", "content": []any{}}})
	writeEvent(w, "response.content_part.added", map[string]any{"type": "response.content_part.added", "response_id": s.responseID, "item_id": s.responseMessageID, "output_index": s.responseTextIndex, "content_index": 0, "part": map[string]any{"type": "output_text", "text": "", "annotations": []any{}}})
}

func (s *streamState) ensureResponseReasoning(w http.ResponseWriter) {
	if s.responseReasoningStart {
		return
	}
	s.responseReasoningStart = true
	s.responseReasoningIndex = s.responseNextOutput
	s.responseNextOutput++
	s.responseReasoningID = newID("rs")
	writeEvent(w, "response.output_item.added", map[string]any{"type": "response.output_item.added", "response_id": s.responseID, "output_index": s.responseReasoningIndex, "item": map[string]any{"id": s.responseReasoningID, "type": "reasoning", "status": "in_progress", "summary": []any{}}})
	writeEvent(w, "response.reasoning_summary_part.added", map[string]any{"type": "response.reasoning_summary_part.added", "response_id": s.responseID, "item_id": s.responseReasoningID, "output_index": s.responseReasoningIndex, "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}})
}

func (s *streamState) messagesEvent(w http.ResponseWriter, event upstream.Event, tool *streamToolCall, _ bool) {
	if event.Kind == upstream.EventToolCall {
		if tool == nil {
			return
		}
		if !s.blockOpen || s.blockKind != upstream.EventToolCall || s.blockToolID != tool.id {
			s.closeBlock(w)
			writeEvent(w, "content_block_start", map[string]any{"type": "content_block_start", "index": s.blockIndex, "content_block": map[string]any{"type": "tool_use", "id": tool.id, "name": tool.name, "input": map[string]any{}}})
			s.blockOpen, s.blockKind, s.blockToolID = true, upstream.EventToolCall, tool.id
		}
		if event.Arguments != "" {
			writeEvent(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": s.blockIndex, "delta": map[string]any{"type": "input_json_delta", "partial_json": event.Arguments}})
		}
		return
	}
	if event.Kind != upstream.EventTextDelta && event.Kind != upstream.EventReasoningDelta && event.Kind != upstream.EventImage && event.Kind != upstream.EventVideo {
		return
	}
	kind := event.Kind
	if kind == upstream.EventImage || kind == upstream.EventVideo {
		kind = upstream.EventTextDelta
	}
	if !s.blockOpen || s.blockKind != kind {
		s.closeBlock(w)
		blockType := "text"
		if kind == upstream.EventReasoningDelta {
			blockType = "thinking"
		}
		writeEvent(w, "content_block_start", map[string]any{"type": "content_block_start", "index": s.blockIndex, "content_block": map[string]any{"type": blockType, blockType: ""}})
		s.blockOpen, s.blockKind = true, kind
	}
	text := event.Text
	if event.Kind == upstream.EventImage {
		text = "\n\n![generated image](" + event.URL + ")"
	} else if event.Kind == upstream.EventVideo {
		text = "\n\n" + event.URL
	}
	deltaType, field := "text_delta", "text"
	if kind == upstream.EventReasoningDelta {
		deltaType, field = "thinking_delta", "thinking"
	}
	writeEvent(w, "content_block_delta", map[string]any{"type": "content_block_delta", "index": s.blockIndex, "delta": map[string]any{"type": deltaType, field: text}})
}

func (s *streamState) closeBlock(w http.ResponseWriter) {
	if !s.blockOpen {
		return
	}
	writeEvent(w, "content_block_stop", map[string]any{"type": "content_block_stop", "index": s.blockIndex})
	s.blockOpen = false
	s.blockToolID = ""
	s.blockIndex++
}

func (s *streamState) finish(w http.ResponseWriter) {
	switch s.operation {
	case upstream.OperationChat:
		finishReason := "stop"
		if len(s.tools) > 0 {
			finishReason = "tool_calls"
		}
		writeData(w, map[string]any{"id": s.responseID, "object": "chat.completion.chunk", "created": time.Now().Unix(), "model": s.model, "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finishReason}}, "usage": chatUsage(s.usage)})
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	case upstream.OperationResponses:
		s.finishResponses(w)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	case upstream.OperationMessages:
		s.closeBlock(w)
		stopReason := "end_turn"
		if len(s.tools) > 0 {
			stopReason = "tool_use"
		}
		writeEvent(w, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil}, "usage": map[string]any{"output_tokens": s.usage.OutputTokens}})
		writeEvent(w, "message_stop", map[string]any{"type": "message_stop"})
	}
}

func (s *streamState) finishResponses(w http.ResponseWriter) {
	output := make([]any, s.responseNextOutput)
	if s.responseReasoningStart {
		text := s.reasoning.String()
		item := map[string]any{"id": s.responseReasoningID, "type": "reasoning", "status": "completed", "summary": []any{map[string]any{"type": "summary_text", "text": text}}}
		writeEvent(w, "response.reasoning_summary_text.done", map[string]any{"type": "response.reasoning_summary_text.done", "response_id": s.responseID, "item_id": s.responseReasoningID, "output_index": s.responseReasoningIndex, "summary_index": 0, "text": text})
		writeEvent(w, "response.reasoning_summary_part.done", map[string]any{"type": "response.reasoning_summary_part.done", "response_id": s.responseID, "item_id": s.responseReasoningID, "output_index": s.responseReasoningIndex, "summary_index": 0, "part": item["summary"].([]any)[0]})
		writeEvent(w, "response.output_item.done", map[string]any{"type": "response.output_item.done", "response_id": s.responseID, "output_index": s.responseReasoningIndex, "item": item})
		output[s.responseReasoningIndex] = item
	}
	if s.responseTextStarted {
		text := s.text.String()
		part := map[string]any{"type": "output_text", "text": text, "annotations": []any{}}
		item := map[string]any{"id": s.responseMessageID, "type": "message", "role": "assistant", "status": "completed", "content": []any{part}}
		writeEvent(w, "response.output_text.done", map[string]any{"type": "response.output_text.done", "response_id": s.responseID, "item_id": s.responseMessageID, "output_index": s.responseTextIndex, "content_index": 0, "text": text})
		writeEvent(w, "response.content_part.done", map[string]any{"type": "response.content_part.done", "response_id": s.responseID, "item_id": s.responseMessageID, "output_index": s.responseTextIndex, "content_index": 0, "part": part})
		writeEvent(w, "response.output_item.done", map[string]any{"type": "response.output_item.done", "response_id": s.responseID, "output_index": s.responseTextIndex, "item": item})
		output[s.responseTextIndex] = item
	}
	for _, tool := range s.tools {
		if tool.outputIndex < 0 {
			continue
		}
		arguments := defaultArguments(tool.arguments.String())
		item := map[string]any{"id": tool.itemID, "type": "function_call", "call_id": tool.id, "name": tool.name, "arguments": arguments, "status": "completed"}
		writeEvent(w, "response.function_call_arguments.done", map[string]any{"type": "response.function_call_arguments.done", "response_id": s.responseID, "item_id": tool.itemID, "output_index": tool.outputIndex, "arguments": arguments})
		writeEvent(w, "response.output_item.done", map[string]any{"type": "response.output_item.done", "response_id": s.responseID, "output_index": tool.outputIndex, "item": item})
		output[tool.outputIndex] = item
	}
	writeEvent(w, "response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": s.responseID, "object": "response", "status": "completed", "model": s.model, "output": output, "usage": responsesUsage(s.usage)}})
}

func (s *streamState) writeError(w http.ResponseWriter, message string) {
	errorType := "server_error"
	errorValue := map[string]any{"message": message, "type": errorType, "code": "upstream_stream_error"}
	if s.operation == upstream.OperationMessages {
		errorValue["type"] = "api_error"
		delete(errorValue, "code")
	}
	writeEvent(w, "error", map[string]any{"type": "error", "error": errorValue})
	if s.operation != upstream.OperationMessages {
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}
}

func writeEvent(w http.ResponseWriter, event string, value any) {
	data, _ := json.Marshal(value)
	_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
}

func writeData(w http.ResponseWriter, value any) {
	data, _ := json.Marshal(value)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
}

func streamResponseID(operation upstream.Operation) string {
	switch operation {
	case upstream.OperationChat:
		return newID("chatcmpl")
	case upstream.OperationResponses:
		return newID("resp")
	default:
		return newID("msg")
	}
}
