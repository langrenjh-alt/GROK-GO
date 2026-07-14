package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/langrenjh-alt/GROK-GO/internal/configevent"
	"github.com/langrenjh-alt/GROK-GO/internal/runtimecfg"
)

type recordingConfigNotifier struct {
	mu              sync.Mutex
	scopes          []configevent.Scope
	lastContextErr  error
	lastHasDeadline bool
}

func (n *recordingConfigNotifier) Notify(ctx context.Context, scope configevent.Scope) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.scopes = append(n.scopes, scope)
	n.lastContextErr = ctx.Err()
	_, n.lastHasDeadline = ctx.Deadline()
	return nil
}

func (n *recordingConfigNotifier) Has(scope configevent.Scope) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, value := range n.scopes {
		if value == scope {
			return true
		}
	}
	return false
}

func TestSettingsAPIReturnsDefaultsActiveAndRestartState(t *testing.T) {
	defaults := runtimecfg.Defaults()
	defaults.PublicBaseURL = "https://old.test"
	runtime := runtimecfg.MustRuntime(defaults, defaults)
	store := NewMemorySettingsStore(map[string]any{"timezone": "UTC"})
	notifier := &recordingConfigNotifier{}
	handler := &Handler{config: Config{Settings: store, RuntimeSettings: runtime, MaxRequestBytes: defaults.MaxRequestBytes, ConfigChanges: notifier}}

	recorder := httptest.NewRecorder()
	handler.saveSettings(recorder, httptest.NewRequest(http.MethodPut, "/settings", strings.NewReader(`{
		"public_base_url":"https://new.test/",
		"request_timeout_seconds":45,
		"cors_origins":"https://b.test/, https://a.test"
	}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("save status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data["public_base_url"] != "https://new.test" || envelope.Data["cors_origins"] != "https://a.test, https://b.test" {
		t.Fatalf("configured settings = %#v", envelope.Data)
	}
	if got := stringSlice(envelope.Data["restart_required"]); !reflect.DeepEqual(got, []string{"public_base_url"}) {
		t.Fatalf("restart_required = %v", got)
	}
	if runtime.Active().RequestTimeoutSeconds != 45 || runtime.Active().PublicBaseURL != "https://old.test" {
		t.Fatalf("active settings = %+v", runtime.Active())
	}
	if !notifier.Has(configevent.ScopeRuntimeSettings) {
		t.Fatal("runtime settings notification was not published")
	}

	recorder = httptest.NewRecorder()
	handler.getSettings(recorder, httptest.NewRequest(http.MethodGet, "/settings", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"defaults"`) || !strings.Contains(recorder.Body.String(), `"active"`) {
		t.Fatalf("get status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestSettingsAPIRejectsUnknownWrongTypeAndBounds(t *testing.T) {
	tests := []string{
		`{"unknown":true}`,
		`{"max_concurrency":"32"}`,
		`{"request_timeout_seconds":0}`,
		`{"cors_origins":"https://example.test/path"}`,
		`{}`,
	}
	for _, body := range tests {
		t.Run(body, func(t *testing.T) {
			defaults := runtimecfg.Defaults()
			handler := &Handler{config: Config{
				Settings:        NewMemorySettingsStore(nil),
				RuntimeSettings: runtimecfg.MustRuntime(defaults, defaults),
				MaxRequestBytes: defaults.MaxRequestBytes,
			}}
			recorder := httptest.NewRecorder()
			handler.saveSettings(recorder, httptest.NewRequest(http.MethodPatch, "/settings", strings.NewReader(body)))
			if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), `"code":"invalid_settings"`) {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func stringSlice(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}
