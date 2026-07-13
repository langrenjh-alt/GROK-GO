package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/langrenjh-alt/GROK-GO/internal/upstream"
)

type MediaLocalizer interface {
	Localize(context.Context, string, string) (string, error)
}

type mediaBase64Localizer interface {
	LocalizeBase64(context.Context, string, string) (string, string, error)
}

func (h *Handler) localizeImageBase64(ctx context.Context, rawURL string) (string, error) {
	if localizer, ok := h.config.Media.(mediaBase64Localizer); ok {
		_, encoded, err := localizer.LocalizeBase64(ctx, "image", rawURL)
		return encoded, err
	}
	encoded, ok := imageDataPayload(rawURL)
	if !ok {
		return "", fmt.Errorf("media cache cannot encode the upstream image URL")
	}
	if h.config.Media != nil {
		if _, err := h.config.Media.Localize(ctx, "image", rawURL); err != nil {
			return "", err
		}
	}
	return encoded, nil
}

func (h *Handler) localizeEvent(ctx context.Context, event upstream.Event) upstream.Event {
	if h.config.Media == nil || event.URL == "" {
		return event
	}
	kind := ""
	if event.Kind == upstream.EventImage {
		kind = "image"
	} else if event.Kind == upstream.EventVideo {
		kind = "video"
	}
	if kind == "" {
		return event
	}
	localized, err := h.config.Media.Localize(ctx, kind, event.URL)
	if err != nil {
		return upstream.Event{Kind: upstream.EventError, Error: fmt.Sprintf("cache upstream %s: %v", kind, err)}
	}
	event.URL = localized
	return event
}

func (h *Handler) localizeBody(ctx context.Context, operation upstream.Operation, body []byte) ([]byte, error) {
	if h.config.Media == nil || len(body) == 0 {
		return body, nil
	}
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	defaultKind := ""
	if operation == upstream.OperationImage || operation == upstream.OperationImageEdit {
		defaultKind = "image"
	} else if operation == upstream.OperationVideo || operation == upstream.OperationVideoStatus {
		defaultKind = "video"
	}
	localized, err := h.localizeValue(ctx, payload, "", defaultKind)
	if err != nil {
		return nil, err
	}
	return json.Marshal(localized)
}

func (h *Handler) localizeValue(ctx context.Context, value any, key, defaultKind string) (any, error) {
	switch value := value.(type) {
	case map[string]any:
		for childKey, child := range value {
			localized, err := h.localizeValue(ctx, child, childKey, defaultKind)
			if err != nil {
				return nil, err
			}
			value[childKey] = localized
		}
		return value, nil
	case []any:
		for index, child := range value {
			localized, err := h.localizeValue(ctx, child, key, defaultKind)
			if err != nil {
				return nil, err
			}
			value[index] = localized
		}
		return value, nil
	case string:
		if !strings.HasPrefix(value, "https://") && !strings.HasPrefix(value, "http://") {
			return value, nil
		}
		kind := mediaKind(key, value, defaultKind)
		if kind == "" {
			return value, nil
		}
		return h.config.Media.Localize(ctx, kind, value)
	default:
		return value, nil
	}
}

func mediaKind(key, rawURL, fallback string) string {
	lowerKey := strings.ToLower(key)
	if strings.Contains(lowerKey, "status") || strings.Contains(lowerKey, "poll") || strings.Contains(lowerKey, "callback") {
		return ""
	}
	value := lowerKey + " " + strings.ToLower(strings.Split(rawURL, "?")[0])
	switch {
	case strings.Contains(value, "video"), strings.HasSuffix(value, ".mp4"), strings.HasSuffix(value, ".webm"):
		return "video"
	case strings.Contains(value, "image"), strings.Contains(value, "thumbnail"), strings.HasSuffix(value, ".png"), strings.HasSuffix(value, ".jpg"), strings.HasSuffix(value, ".jpeg"), strings.HasSuffix(value, ".webp"):
		return "image"
	default:
		return fallback
	}
}
