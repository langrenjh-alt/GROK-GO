package upstream

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

type sseToolState struct {
	id            string
	itemID        string
	name          string
	index         int
	announced     bool
	argumentsSeen bool
}

type sseParser struct {
	tools            map[string]*sseToolState
	nextTool         int
	imageHashes      map[string][32]byte
	done             bool
	reverseToolNames map[string]string
}

func newSSEParser() *sseParser {
	return newSSEParserWithToolNames(nil)

}

func newSSEParserWithToolNames(reverseToolNames map[string]string) *sseParser {
	return &sseParser{tools: make(map[string]*sseToolState), imageHashes: make(map[string][32]byte), reverseToolNames: reverseToolNames}
}

func (p *sseParser) parse(eventName string, data []byte) []Event {
	if p.done {
		return nil
	}
	if strings.TrimSpace(string(data)) == "[DONE]" {
		p.done = true
		return []Event{{Kind: EventDone}}
	}
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil {
		return nil
	}
	typeName := stringValue(payload, "type")
	if typeName == "" {
		typeName = strings.TrimSpace(eventName)
	}
	raw := append([]byte(nil), data...)
	if strings.Contains(typeName, "error") || payload["error"] != nil {
		return []Event{{Kind: EventError, Error: errorText(payload), Raw: raw}}
	}

	switch typeName {
	case "response.output_item.added", "response.output_item.done":
		item, _ := payload["item"].(map[string]any)
		if item != nil && stringValue(item, "type") == "function_call" {
			return p.functionItem(payload, item, typeName == "response.output_item.done", raw)
		}
		if item != nil && stringValue(item, "type") == "image_generation_call" {
			return p.imageItem(payload, item, raw)
		}
	case "response.function_call_arguments.delta":
		state := p.toolState(payload, nil, true)
		state.argumentsSeen = true
		return []Event{{Kind: EventToolCall, ID: state.id, ItemID: state.itemID, Name: state.name, Arguments: stringValue(payload, "delta"), Index: state.index, Raw: raw}}
	case "response.function_call_arguments.done":
		state := p.toolState(payload, nil, true)
		if state.argumentsSeen {
			return nil
		}
		state.argumentsSeen = true
		return []Event{{Kind: EventToolCall, ID: state.id, ItemID: state.itemID, Name: state.name, Arguments: stringValue(payload, "arguments"), Index: state.index, Raw: raw}}
	case "response.image_generation_call.partial_image":
		return p.imageItem(payload, nil, raw)
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		return textEvent(EventReasoningDelta, stringValue(payload, "delta", "text"), raw)
	case "response.output_text.delta", "response.content.delta":
		return textEvent(EventTextDelta, stringValue(payload, "delta", "text"), raw)
	case "response.completed":
		p.done = true
		events := usageEvents(payload)
		return append(events, Event{Kind: EventDone, Raw: raw})
	case "response.failed", "response.incomplete":
		p.done = true
		return []Event{{Kind: EventError, Error: errorText(payload), Raw: raw}}
	}

	if events, handled := p.grokWebEvents(payload, raw); handled {
		return events
	}
	if delta := chatDelta(payload); delta != "" {
		return []Event{{Kind: EventTextDelta, Text: delta, Raw: raw}}
	}
	return mediaEvents(payload, raw)
}

func (p *sseParser) functionItem(event, item map[string]any, done bool, raw []byte) []Event {
	state := p.toolState(event, item, true)
	arguments := stringValue(item, "arguments")
	if state.announced {
		if done && arguments != "" && !state.argumentsSeen {
			state.argumentsSeen = true
			return []Event{{Kind: EventToolCall, ID: state.id, ItemID: state.itemID, Name: state.name, Arguments: arguments, Index: state.index, Raw: raw}}
		}
		return nil
	}
	state.announced = true
	state.argumentsSeen = arguments != ""
	return []Event{{Kind: EventToolCall, ID: state.id, ItemID: state.itemID, Name: state.name, Arguments: arguments, Index: state.index, Raw: raw}}
}

func (p *sseParser) toolState(event, item map[string]any, create bool) *sseToolState {
	keys := toolKeys(event, item)
	for _, key := range keys {
		if state := p.tools[key]; state != nil {
			p.bindToolKeys(state, keys)
			if item != nil {
				if value := stringValue(item, "call_id", "id"); value != "" {
					state.id = value
				}
				if value := stringValue(item, "id"); value != "" {
					state.itemID = value
				}
				if value := stringValue(item, "name"); value != "" {
					state.name = p.restoreToolName(value)
				}
			}
			return state
		}
	}
	index := p.nextTool
	if !create && p.nextTool > 0 {
		index = p.nextTool - 1
	} else {
		p.nextTool++
	}
	state := &sseToolState{index: index}
	if item != nil {
		state.id = stringValue(item, "call_id", "id")
		state.itemID = stringValue(item, "id")
		state.name = p.restoreToolName(stringValue(item, "name"))
	}
	if state.id == "" {
		state.id = stringValue(event, "call_id", "item_id", "id")
	}
	if state.itemID == "" {
		state.itemID = stringValue(event, "item_id")
	}
	if state.itemID == "" {
		state.itemID = "fc_" + strings.TrimPrefix(state.id, "call_")
	}
	if state.id == "" {
		state.id = fmt.Sprintf("call_%d", index)
	}
	p.bindToolKeys(state, keys)
	return state
}

func (p *sseParser) restoreToolName(name string) string {
	if original := p.reverseToolNames[name]; original != "" {
		return original
	}
	return name
}

func (p *sseParser) bindToolKeys(state *sseToolState, keys []string) {
	for _, key := range keys {
		p.tools[key] = state
	}
	p.tools["call:"+state.id] = state
}

func toolKeys(event, item map[string]any) []string {
	result := make([]string, 0, 5)
	appendKey := func(prefix, value string) {
		if value != "" {
			result = append(result, prefix+":"+value)
		}
	}
	appendKey("item", stringValue(event, "item_id"))
	appendKey("call", stringValue(event, "call_id"))
	if item != nil {
		appendKey("item", stringValue(item, "id"))
		appendKey("call", stringValue(item, "call_id"))
	}
	if value, ok := event["output_index"]; ok {
		appendKey("output", fmt.Sprint(value))
	}
	return result
}

func (p *sseParser) imageItem(event, item map[string]any, raw []byte) []Event {
	value := stringValue(event, "partial_image_b64")
	itemID := stringValue(event, "item_id")
	format := stringValue(event, "output_format")
	if item != nil {
		if value == "" {
			value = stringValue(item, "result")
		}
		if itemID == "" {
			itemID = stringValue(item, "id")
		}
		if format == "" {
			format = stringValue(item, "output_format")
		}
	}
	if value == "" {
		return nil
	}
	digest := sha256.Sum256([]byte(value))
	if itemID != "" && p.imageHashes[itemID] == digest {
		return nil
	}
	if itemID != "" {
		p.imageHashes[itemID] = digest
	}
	return []Event{{Kind: EventImage, URL: "data:" + imageMIME(format) + ";base64," + value, Raw: raw}}
}

func (p *sseParser) grokWebEvents(payload map[string]any, raw []byte) ([]Event, bool) {
	result, _ := payload["result"].(map[string]any)
	response, _ := result["response"].(map[string]any)
	if response == nil {
		return nil, false
	}
	if nested, ok := response["error"].(map[string]any); ok {
		return []Event{{Kind: EventError, Error: errorText(nested), Raw: raw}}, true
	}
	events := make([]Event, 0)
	if card, ok := response["cardAttachment"].(map[string]any); ok {
		events = append(events, generatedImageCard(card, raw)...)
	}
	if stream, ok := response["streamingVideoGenerationResponse"].(map[string]any); ok {
		if value := stringValue(stream, "videoUrl"); value != "" {
			events = append(events, Event{Kind: EventVideo, URL: absoluteAssetURL(value), Raw: raw})
		}
		if value := stringValue(stream, "thumbnailImageUrl"); value != "" {
			events = append(events, Event{Kind: EventImage, URL: absoluteAssetURL(value), Raw: raw})
		}
	}
	if stream, ok := response["streamingImageGenerationResponse"].(map[string]any); ok {
		if value := stringValue(stream, "imageUrl"); value != "" {
			events = append(events, Event{Kind: EventImage, URL: absoluteAssetURL(value), Raw: raw})
		}
	}
	if token, ok := response["token"].(string); ok && token != "" {
		kind := EventTextDelta
		if thinking, _ := response["isThinking"].(bool); thinking {
			kind = EventReasoningDelta
		}
		events = append(events, Event{Kind: kind, Text: token, Raw: raw})
	}
	events = append(events, mediaEvents(response, raw)...)
	events = deduplicateMedia(events)
	softStop, _ := response["isSoftStop"].(bool)
	if finalMetadata, ok := response["finalMetadata"].(map[string]any); ok {
		events = append(events, usageEvents(finalMetadata)...)
	}
	if softStop || response["finalMetadata"] != nil {
		p.done = true
		events = append(events, Event{Kind: EventDone, Raw: raw})
	}
	return events, true
}

func generatedImageCard(card map[string]any, raw []byte) []Event {
	data := card["jsonData"]
	var decoded map[string]any
	switch value := data.(type) {
	case string:
		if json.Unmarshal([]byte(value), &decoded) != nil {
			return nil
		}
	case map[string]any:
		decoded = value
	default:
		return nil
	}
	chunk, _ := decoded["image_chunk"].(map[string]any)
	if chunk == nil || intValue(chunk["progress"]) != 100 {
		return nil
	}
	if moderated, _ := chunk["moderated"].(bool); moderated {
		return nil
	}
	value := stringValue(chunk, "imageUrl", "image_url", "url")
	if value == "" {
		return nil
	}
	if !strings.HasPrefix(value, "http://") && !strings.HasPrefix(value, "https://") {
		value = "https://assets.grok.com/" + strings.TrimLeft(value, "/")
	}
	return []Event{{Kind: EventImage, URL: value, Raw: raw}}
}

func responseObjectEvents(payload map[string]any, raw []byte, reverseToolNames map[string]string) []Event {
	events := make([]Event, 0)
	output := valueArray(payload["output"])
	for _, rawItem := range output {
		item, _ := rawItem.(map[string]any)
		if item == nil {
			continue
		}
		switch stringValue(item, "type") {
		case "reasoning":
			for _, rawPart := range valueArray(item["summary"]) {
				part, _ := rawPart.(map[string]any)
				if text := stringValue(part, "text"); text != "" {
					events = append(events, Event{Kind: EventReasoningDelta, Text: text})
				}
			}
		case "message":
			for _, rawPart := range valueArray(item["content"]) {
				part, _ := rawPart.(map[string]any)
				if text := stringValue(part, "text"); text != "" {
					events = append(events, Event{Kind: EventTextDelta, Text: text})
				}
			}
		case "function_call":
			name := stringValue(item, "name")
			if original := reverseToolNames[name]; original != "" {
				name = original
			}
			events = append(events, Event{Kind: EventToolCall, ID: stringValue(item, "call_id", "id"), ItemID: stringValue(item, "id"), Name: name, Arguments: stringValue(item, "arguments"), Index: len(events)})
		case "image_generation_call":
			if value := stringValue(item, "result"); value != "" {
				events = append(events, Event{Kind: EventImage, URL: "data:" + imageMIME(stringValue(item, "output_format")) + ";base64," + value})
			}
		}
	}
	if len(output) == 0 {
		if text := nonStreamText(payload); text != "" {
			events = append(events, Event{Kind: EventTextDelta, Text: text})
		}
	}
	events = append(events, mediaEvents(payload, raw)...)
	return deduplicateMedia(events)
}

func deduplicateMedia(events []Event) []Event {
	seen := make(map[string]struct{})
	result := make([]Event, 0, len(events))
	for _, event := range events {
		if event.Kind == EventImage || event.Kind == EventVideo {
			key := string(event.Kind) + "\x00" + event.URL
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
		}
		result = append(result, event)
	}
	return result
}

func textEvent(kind EventKind, text string, raw []byte) []Event {
	if text == "" {
		return nil
	}
	return []Event{{Kind: kind, Text: text, Raw: raw}}
}

func imageMIME(format string) string {
	value := strings.ToLower(strings.TrimSpace(format))
	if strings.Contains(value, "/") {
		return value
	}
	switch value {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	case "gif":
		return "image/gif"
	default:
		return "image/png"
	}
}
