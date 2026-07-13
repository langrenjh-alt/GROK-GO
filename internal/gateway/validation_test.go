package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/langrenjh-alt/GROK-GO/internal/upstream"
)

func TestInvalidMediaPayloadsFailBeforeUpstream(t *testing.T) {
	var calls atomic.Int32
	handler := testGateway(t, upstream.ClientFunc(func(context.Context, upstream.Request) (*upstream.Response, error) {
		calls.Add(1)
		return &upstream.Response{StatusCode: http.StatusOK}, nil
	}))
	tests := []struct {
		path  string
		body  string
		param string
	}{
		{"/v1/images/generations", `{"model":"grok-image","prompt":"","n":1}`, "prompt"},
		{"/v1/images/generations", `{"model":"grok-image","prompt":"x","n":"many"}`, "n"},
		{"/v1/images/generations", `{"model":"grok-image","prompt":"x","response_format":"raw"}`, "response_format"},
		{"/v1/images/edits", `{"model":"grok-image-edit","prompt":"x","n":1}`, "image"},
		{"/v1/videos", `{"model":"grok-video","prompt":"x","seconds":"seven"}`, "seconds"},
		{"/v1/videos", `{"model":"grok-video","prompt":"x","seconds":7}`, "seconds"},
		{"/v1/videos", `{"model":"grok-video","prompt":"x","size":"640x480"}`, "size"},
	}
	for _, test := range tests {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body)))
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"param":"`+test.param+`"`) {
			t.Errorf("%s => %d %s", test.body, response.Code, response.Body.String())
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid requests reached upstream %d times", calls.Load())
	}
}

func TestValidateImageEditPayload(t *testing.T) {
	payload := map[string]any{"prompt": "edit", "image": []any{"data:image/png;base64,aW1hZ2U="}, "n": "2", "size": "1024x1024", "response_format": "b64_json"}
	if issue := validateOperationPayload(upstream.OperationImageEdit, payload); issue != nil {
		t.Fatalf("valid image edit rejected: %+v", issue)
	}
	if issue := validateOperationPayload(upstream.OperationImageEdit, map[string]any{"prompt": "edit"}); issue == nil || issue.param != "image" {
		t.Fatalf("missing image issue = %+v", issue)
	}
}
