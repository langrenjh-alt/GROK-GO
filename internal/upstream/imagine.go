package upstream

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

var imagineImageID = regexp.MustCompile(`(?i)/images/([a-f0-9-]+)\.(png|jpe?g|webp)`)

type imagineSlot struct {
	url       string
	blob      string
	format    string
	completed bool
}

func useImagineWebSocket(request Request) bool {
	return request.Operation == OperationImage && request.CredentialKind == domain.CredentialGrokSSO && !strings.Contains(strings.ToLower(request.Model), "lite")
}

func (c *HTTPClient) doImagine(ctx context.Context, input Request) (*Response, error) {
	prompt, count, ratio, pro, err := imagineInput(input)
	if err != nil {
		return nil, err
	}
	streamCtx, cancel := context.WithTimeout(ctx, c.requestTimeout())

	client := c.client
	if strings.TrimSpace(input.ProxyURL) != "" {
		client, err = c.clientForProxy(input.ProxyURL)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("configure account proxy: %w", err)
		}
	}
	dummy, err := http.NewRequestWithContext(streamCtx, http.MethodGet, c.config.ImagineURL, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	if err := (GrokSSOAdapter{}).Apply(dummy, input.Credentials); err != nil {
		cancel()
		return nil, err
	}
	connection, response, err := websocket.Dial(streamCtx, c.config.ImagineURL, &websocket.DialOptions{
		HTTPClient:      client,
		HTTPHeader:      dummy.Header,
		CompressionMode: websocket.CompressionContextTakeover,
	})
	if err != nil {
		cancel()
		if response != nil {
			defer response.Body.Close()
			return &Response{StatusCode: response.StatusCode, Header: response.Header.Clone()}, nil
		}
		return nil, fmt.Errorf("connect imagine websocket: %w", err)
	}
	connection.SetReadLimit(c.config.MaxResponseSize)

	events := make(chan Event, 32)
	go func() {
		defer cancel()
		defer close(events)
		defer connection.Close(websocket.StatusNormalClosure, "complete")
		if err := writeImagineRequest(streamCtx, connection, prompt, ratio, pro); err != nil {
			events <- Event{Kind: EventError, Error: err.Error()}
			return
		}
		readImagineEvents(streamCtx, connection, count, events)
	}()
	return &Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Events: events}, nil
}

func imagineInput(input Request) (prompt string, count int, ratio string, pro bool, err error) {
	var payload map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(input.Body)))
	decoder.UseNumber()
	if err = decoder.Decode(&payload); err != nil {
		return "", 0, "", false, fmt.Errorf("decode image request: %w", err)
	}
	prompt = strings.TrimSpace(stringValue(payload, "prompt"))
	if prompt == "" {
		return "", 0, "", false, errors.New("image prompt is required")
	}
	count = intValue(payload["n"])
	if count == 0 {
		count = 1
	}
	if count < 1 || count > 4 {
		return "", 0, "", false, errors.New("image count must be between 1 and 4")
	}
	ratio = imagineAspectRatio(stringValue(payload, "size", "aspect_ratio"))
	pro = strings.Contains(strings.ToLower(input.Model+" "+input.UpstreamModel), "pro")
	return prompt, count, ratio, pro, nil
}

func writeImagineRequest(ctx context.Context, connection *websocket.Conn, prompt, ratio string, pro bool) error {
	reset := map[string]any{
		"type": "conversation.item.create", "timestamp": time.Now().UnixMilli(),
		"item": map[string]any{"type": "message", "content": []any{map[string]any{"type": "reset"}}},
	}
	request := map[string]any{
		"type": "conversation.item.create", "timestamp": time.Now().UnixMilli(),
		"item": map[string]any{"type": "message", "content": []any{map[string]any{
			"requestId": randomUUID(), "text": prompt, "type": "input_text",
			"properties": map[string]any{
				"section_count": 0, "is_kids_mode": false, "enable_nsfw": true,
				"skip_upsampler": false, "enable_side_by_side": true, "is_initial": false,
				"aspect_ratio": ratio, "enable_pro": pro,
			},
		}}},
	}
	for _, value := range []any{reset, request} {
		encoded, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if err := connection.Write(ctx, websocket.MessageText, encoded); err != nil {
			return fmt.Errorf("write imagine request: %w", err)
		}
	}
	return nil
}

func readImagineEvents(ctx context.Context, connection *websocket.Conn, requested int, output chan<- Event) {
	slots := make(map[string]*imagineSlot)
	collected := 0
	for collected < requested {
		messageType, data, err := connection.Read(ctx)
		if err != nil {
			if collected > 0 && websocket.CloseStatus(err) != -1 {
				output <- Event{Kind: EventDone}
				return
			}
			output <- Event{Kind: EventError, Error: fmt.Sprintf("read imagine websocket: %v", err)}
			return
		}
		if messageType != websocket.MessageText {
			continue
		}
		var frame map[string]any
		if json.Unmarshal(data, &frame) != nil {
			continue
		}
		switch stringValue(frame, "type") {
		case "json":
			id := stringValue(frame, "image_id", "job_id")
			if id == "" {
				continue
			}
			slot := slots[id]
			if slot == nil {
				slot = &imagineSlot{}
				slots[id] = slot
			}
			if stringValue(frame, "current_status") != "completed" || slot.completed {
				continue
			}
			slot.completed = true
			if moderated, _ := frame["moderated"].(bool); moderated {
				continue
			}
			url := imagineSlotURL(slot)
			if url == "" {
				continue
			}
			output <- Event{Kind: EventImage, URL: url, Raw: append([]byte(nil), data...)}
			collected++
		case "image":
			id, format := parseImagineImageURL(stringValue(frame, "url"))
			if id == "" {
				id = stringValue(frame, "image_id")
			}
			if id == "" {
				continue
			}
			slot := slots[id]
			if slot == nil {
				slot = &imagineSlot{}
				slots[id] = slot
			}
			slot.url = stringValue(frame, "url")
			slot.blob = stringValue(frame, "blob")
			slot.format = format
		case "error":
			output <- Event{Kind: EventError, Error: firstNonEmpty(stringValue(frame, "err_msg", "message", "error"), "imagine upstream error"), Raw: append([]byte(nil), data...)}
			return
		}
	}
	output <- Event{Kind: EventDone}
}

func imagineSlotURL(slot *imagineSlot) string {
	if slot.blob != "" {
		return "data:" + imageMIME(slot.format) + ";base64," + slot.blob
	}
	value := strings.TrimSpace(slot.url)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return value
	}
	return "https://assets.grok.com/" + strings.TrimLeft(value, "/")
}

func parseImagineImageURL(value string) (string, string) {
	match := imagineImageID.FindStringSubmatch(value)
	if len(match) != 3 {
		return "", "png"
	}
	return match[1], match[2]
}

func imagineAspectRatio(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1280x720", "16:9":
		return "16:9"
	case "720x1280", "9:16":
		return "9:16"
	case "1792x1024", "3:2":
		return "3:2"
	case "1024x1792", "2:3", "":
		return "2:3"
	case "1024x1024", "1:1":
		return "1:1"
	default:
		return "2:3"
	}
}

func randomUUID() string {
	var value [16]byte
	_, _ = rand.Read(value[:])
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
