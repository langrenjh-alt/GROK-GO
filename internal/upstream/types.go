package upstream

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

type Operation string

const (
	OperationChat        Operation = "chat"
	OperationResponses   Operation = "responses"
	OperationMessages    Operation = "messages"
	OperationImage       Operation = "image"
	OperationImageEdit   Operation = "image_edit"
	OperationVideo       Operation = "video"
	OperationVideoStatus Operation = "video_status"
)

type EventKind string

const (
	EventTextDelta      EventKind = "text_delta"
	EventReasoningDelta EventKind = "reasoning_delta"
	EventToolCall       EventKind = "tool_call"
	EventImage          EventKind = "image"
	EventVideo          EventKind = "video"
	EventUsage          EventKind = "usage"
	EventDone           EventKind = "done"
	EventError          EventKind = "error"
)

type Usage struct {
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	CachedTokens int64 `json:"cached_tokens"`
}

type Event struct {
	Kind      EventKind       `json:"kind"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	ItemID    string          `json:"item_id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Arguments string          `json:"arguments,omitempty"`
	Index     int             `json:"index,omitempty"`
	URL       string          `json:"url,omitempty"`
	Usage     Usage           `json:"usage,omitempty"`
	Error     string          `json:"error,omitempty"`
	Raw       json.RawMessage `json:"raw,omitempty"`
}

type Request struct {
	Operation      Operation
	Model          string
	UpstreamModel  string
	PromptCacheKey string
	CredentialKind domain.CredentialKind
	Credentials    domain.Credentials
	ProxyURL       string
	Body           json.RawMessage
	Headers        http.Header
	Stream         bool
	VideoID        string
	StatusURL      string
}

type Response struct {
	StatusCode int
	Header     http.Header
	Body       json.RawMessage
	Events     <-chan Event
}

type Client interface {
	Do(context.Context, Request) (*Response, error)
}

type ClientFunc func(context.Context, Request) (*Response, error)

func (f ClientFunc) Do(ctx context.Context, request Request) (*Response, error) {
	return f(ctx, request)
}

func Events(values ...Event) <-chan Event {
	result := make(chan Event, len(values))
	for _, value := range values {
		result <- value
	}
	close(result)
	return result
}

type HTTPError struct {
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	if e.Body != "" {
		return e.Body
	}
	return http.StatusText(e.StatusCode)
}
