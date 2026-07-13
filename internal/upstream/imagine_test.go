package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

func TestHTTPClientUsesImagineWebSocketForProImages(t *testing.T) {
	requests := make(chan map[string]any, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Cookie"), "sso=secret") || r.Header.Get("Origin") != "https://grok.com" {
			t.Errorf("unexpected imagine headers: %#v", r.Header)
		}
		connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer connection.Close(websocket.StatusNormalClosure, "done")
		for range 2 {
			_, data, readErr := connection.Read(r.Context())
			if readErr != nil {
				t.Errorf("read imagine request: %v", readErr)
				return
			}
			var payload map[string]any
			_ = json.Unmarshal(data, &payload)
			requests <- payload
		}
		frames := []string{
			`{"type":"json","current_status":"start_stage","image_id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","order":0,"width":1024,"height":1024}`,
			`{"type":"image","url":"/images/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee.jpg","blob":"aW1hZ2U=","percentage_complete":100}`,
			`{"type":"json","current_status":"completed","image_id":"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee","order":0,"moderated":false}`,
		}
		for _, frame := range frames {
			if err := connection.Write(r.Context(), websocket.MessageText, []byte(frame)); err != nil {
				t.Errorf("write imagine frame: %v", err)
				return
			}
		}
	}))
	defer server.Close()

	client := NewHTTPClient(HTTPClientConfig{
		Client: server.Client(), ImagineURL: "ws" + strings.TrimPrefix(server.URL, "http"), RequestTimeout: 5 * time.Second,
	})
	response, err := client.Do(context.Background(), Request{
		Operation: OperationImage, Model: "grok-imagine-image-pro", UpstreamModel: "grok-imagine-image-pro",
		CredentialKind: domain.CredentialGrokSSO, Credentials: domain.Credentials{SSO: "secret"},
		Body: json.RawMessage(`{"prompt":"studio product","n":1,"size":"1024x1024"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var events []Event
	for event := range response.Events {
		events = append(events, event)
	}
	if len(events) != 2 || events[0].Kind != EventImage || events[0].URL != "data:image/jpeg;base64,aW1hZ2U=" || events[1].Kind != EventDone {
		t.Fatalf("events = %#v", events)
	}

	reset := <-requests
	request := <-requests
	resetItem := reset["item"].(map[string]any)
	resetContent := resetItem["content"].([]any)[0].(map[string]any)
	if resetContent["type"] != "reset" {
		t.Fatalf("reset frame = %#v", reset)
	}
	requestItem := request["item"].(map[string]any)
	requestContent := requestItem["content"].([]any)[0].(map[string]any)
	properties := requestContent["properties"].(map[string]any)
	if requestContent["text"] != "studio product" || properties["aspect_ratio"] != "1:1" || properties["enable_pro"] != true {
		t.Fatalf("request frame = %#v", request)
	}
}

func TestImagineInputValidatesCountAndPrompt(t *testing.T) {
	_, count, _, _, err := imagineInput(Request{Body: json.RawMessage(`{"prompt":"x","n":"4"}`)})
	if err != nil || count != 4 {
		t.Fatalf("multipart-style count = %d, %v", count, err)
	}
	for _, body := range []string{`{"prompt":"","n":1}`, `{"prompt":"x","n":5}`} {
		if _, _, _, _, err := imagineInput(Request{Body: json.RawMessage(body)}); err == nil {
			t.Fatalf("invalid body accepted: %s", body)
		}
	}
}

func TestImagineWebSocketAcceptsImageFramesAboveLibraryDefaultLimit(t *testing.T) {
	blob := strings.Repeat("a", 64<<10)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connection, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		defer connection.Close(websocket.StatusNormalClosure, "done")
		for range 2 {
			if _, _, err := connection.Read(r.Context()); err != nil {
				t.Errorf("read imagine request: %v", err)
				return
			}
		}
		frames := []map[string]any{
			{"type": "image", "image_id": "large-image", "blob": blob},
			{"type": "json", "current_status": "completed", "image_id": "large-image", "moderated": false},
		}
		for _, frame := range frames {
			encoded, _ := json.Marshal(frame)
			if err := connection.Write(r.Context(), websocket.MessageText, encoded); err != nil {
				t.Errorf("write imagine frame: %v", err)
				return
			}
		}
	}))
	defer server.Close()

	client := NewHTTPClient(HTTPClientConfig{Client: server.Client(), ImagineURL: "ws" + strings.TrimPrefix(server.URL, "http"), MaxResponseSize: 1 << 20, RequestTimeout: 5 * time.Second})
	response, err := client.Do(context.Background(), Request{
		Operation: OperationImage, Model: "grok-imagine-image", UpstreamModel: "grok-imagine-image",
		CredentialKind: domain.CredentialGrokSSO, Credentials: domain.Credentials{SSO: "secret"}, Body: json.RawMessage(`{"prompt":"large frame","n":1}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	events := make([]Event, 0, 2)
	for event := range response.Events {
		events = append(events, event)
	}
	if len(events) != 2 || events[0].Kind != EventImage || !strings.HasSuffix(events[0].URL, blob) || events[1].Kind != EventDone {
		t.Fatalf("events = %#v", events)
	}
}
