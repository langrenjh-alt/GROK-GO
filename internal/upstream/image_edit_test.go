package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

func TestHTTPClientRunsGrokImageEditPreflight(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		switch r.URL.Path {
		case "/rest/app-chat/upload-file":
			if payload["fileMimeType"] != "image/png" || payload["content"] != "aW1hZ2U=" {
				t.Errorf("upload payload = %#v", payload)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"fileMetadataId":"asset_1"}`)
		case "/rest/media/post/create":
			if payload["mediaType"] != "MEDIA_POST_TYPE_IMAGE" || payload["prompt"] != "make it blue" {
				t.Errorf("media payload = %#v", payload)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"post":{"id":"post_1","originalPrompt":"make it blue"}}`)
		case "/rest/app-chat/conversations/new":
			metadata := payload["responseMetadata"].(map[string]any)
			override := metadata["modelConfigOverride"].(map[string]any)
			modelMap := override["modelMap"].(map[string]any)
			config := modelMap["imageEditModelConfig"].(map[string]any)
			references := config["imageReferences"].([]any)
			if config["parentPostId"] != "post_1" || len(references) != 1 || references[0] != "https://assets.grok.com/users/test/asset_1/content" || payload["imageGenerationCount"] != float64(1) {
				t.Errorf("edit payload = %#v", payload)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, `data: {"result":{"response":{"streamingImageGenerationResponse":{"progress":100,"imageUrl":"https://assets.grok.com/edited.png","imageIndex":0}}}}`+"\n\n")
			_, _ = io.WriteString(w, `data: {"result":{"response":{"finalMetadata":{}}}}`+"\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client := NewHTTPClient(HTTPClientConfig{Client: server.Client(), GrokBaseURL: server.URL})
	response, err := client.Do(context.Background(), Request{
		Operation: OperationImageEdit, Model: "grok-imagine-image-edit", UpstreamModel: "grok-imagine-image-edit",
		CredentialKind: domain.CredentialGrokSSO, Credentials: domain.Credentials{SSO: "secret", UserID: "test", BaseURL: server.URL},
		Body: json.RawMessage(`{"prompt":"make it blue","image":[{"filename":"input.png","content_type":"image/png","data":"data:image/png;base64,aW1hZ2U="}],"n":"1"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	var events []Event
	for event := range response.Events {
		events = append(events, event)
	}
	if len(events) != 2 || events[0].Kind != EventImage || events[0].URL != "https://assets.grok.com/edited.png" || events[1].Kind != EventDone {
		t.Fatalf("events = %#v", events)
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(paths, ",")
	for _, path := range []string{"/rest/app-chat/upload-file", "/rest/media/post/create", "/rest/app-chat/conversations/new"} {
		if !strings.Contains(joined, path) {
			t.Fatalf("missing %s in %v", path, paths)
		}
	}
}

func TestParseImageDataURIRejectsInvalidData(t *testing.T) {
	for _, value := range []string{"https://example.test/a.png", "data:image/png,raw", "data:image/png;base64,%%%"} {
		if _, _, _, err := parseImageDataURI(value); err == nil {
			t.Fatalf("invalid image input accepted: %q", value)
		}
	}
}

func TestResolveUploadedReference(t *testing.T) {
	tests := []struct {
		name            string
		fileID, fileURI string
		userID          string
		want            string
	}{
		{name: "relative URI", fileID: "ignored", fileURI: "/users/test/asset/content", want: "https://assets.grok.com/users/test/asset/content"},
		{name: "absolute URI", fileURI: "https://cdn.example.test/asset.png", want: "https://cdn.example.test/asset.png"},
		{name: "metadata ID", fileID: "asset/1", userID: "user name", want: "https://assets.grok.com/users/user%20name/asset%2F1/content"},
		{name: "metadata ID without user", fileID: "asset_1", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := resolveUploadedReference(test.fileID, test.fileURI, test.userID); got != test.want {
				t.Fatalf("resolveUploadedReference() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCredentialUserIDSupportsExplicitAndCookieValues(t *testing.T) {
	if got := credentialUserID(domain.Credentials{UserID: "explicit", SSO: "sso=secret; x-userid=cookie"}); got != "explicit" {
		t.Fatalf("explicit user ID = %q", got)
	}
	credentials := domain.Credentials{SSO: "sso=secret; x-userid=cookie-user"}
	if got := credentialUserID(credentials); got != "cookie-user" {
		t.Fatalf("cookie user ID = %q", got)
	}
	if cookie := ssoCookie(credentials); !strings.Contains(cookie, "x-userid=cookie-user") {
		t.Fatalf("outbound cookie omitted x-userid: %q", cookie)
	}
}
