package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteErrorForRequestUsesAnthropicEnvelope(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	WriteErrorForRequest(response, request, http.StatusUnauthorized, "invalid_api_key", "invalid key")

	var payload struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusUnauthorized || payload.Type != "error" || payload.Error.Type != "authentication_error" || payload.Error.Message != "invalid key" {
		t.Fatalf("response = %d %+v", response.Code, payload)
	}
}
