package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

func TestHTTPClientRunsGrokVideoAsBackgroundJob(t *testing.T) {
	background, cancel := context.WithCancel(context.Background())
	defer cancel()
	videoPayload := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		switch r.URL.Path {
		case "/rest/media/post/create":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"post":{"id":"post_1"}}`)
		case "/rest/app-chat/conversations/new":
			videoPayload <- payload
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"result":{"response":{"streamingVideoGenerationResponse":{"progress":40,"videoPostId":"video_post_1"}}}}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"result":{"response":{"streamingVideoGenerationResponse":{"progress":100,"videoPostId":"video_post_1","videoUrl":"/users/test/video.mp4","thumbnailImageUrl":"/users/test/thumb.jpg"}}}}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"result":{"response":{"finalMetadata":{}}}}`+"\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewHTTPClient(HTTPClientConfig{Client: server.Client(), BackgroundContext: background, GrokBaseURL: server.URL, RequestTimeout: time.Second, VideoTimeout: 3 * time.Second})
	created, err := client.Do(context.Background(), Request{
		Operation: OperationVideo, Model: "grok-imagine-video", UpstreamModel: "grok-imagine-video",
		CredentialKind: domain.CredentialGrokSSO, Credentials: domain.Credentials{SSO: "secret", BaseURL: server.URL},
		Body: json.RawMessage(`{"prompt":"waves","seconds":6,"size":"1280x720","preset":"normal"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var initial map[string]any
	_ = json.Unmarshal(created.Body, &initial)
	id, _ := initial["id"].(string)
	if id == "" || initial["status"] != "queued" {
		t.Fatalf("initial job = %s", created.Body)
	}

	deadline := time.Now().Add(2 * time.Second)
	var status map[string]any
	for time.Now().Before(deadline) {
		response := client.grokVideoStatus(id)
		_ = json.Unmarshal(response.Body, &status)
		if status["status"] == "completed" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if status["status"] != "completed" || status["progress"].(float64) != 100 || status["url"] != "https://assets.grok.com/users/test/video.mp4" {
		t.Fatalf("completed job = %#v", status)
	}

	payload := <-videoPayload
	metadata := payload["responseMetadata"].(map[string]any)
	override := metadata["modelConfigOverride"].(map[string]any)
	modelMap := override["modelMap"].(map[string]any)
	config := modelMap["videoGenModelConfig"].(map[string]any)
	if config["parentPostId"] != "post_1" || config["aspectRatio"] != "16:9" || config["videoLength"].(float64) != 6 || config["resolutionName"] != "720p" || !strings.Contains(payload["message"].(string), "--mode=normal") {
		t.Fatalf("video generation payload = %#v", payload)
	}
}

func TestParseGrokVideoInputValidation(t *testing.T) {
	valid, err := parseGrokVideoInput(json.RawMessage(`{"prompt":"x","seconds":"10","size":"1280x720"}`))
	if err != nil || valid.seconds != 10 {
		t.Fatalf("multipart-style seconds = %+v, %v", valid, err)
	}
	for _, body := range []string{
		`{"prompt":"","seconds":6,"size":"1280x720"}`,
		`{"prompt":"x","seconds":7,"size":"1280x720"}`,
		`{"prompt":"x","seconds":6,"size":"640x480"}`,
		`{"prompt":"x","seconds":6,"size":"1280x720","preset":"unknown"}`,
	} {
		if _, err := parseGrokVideoInput(json.RawMessage(body)); err == nil {
			t.Fatalf("invalid video request accepted: %s", body)
		}
	}
}
