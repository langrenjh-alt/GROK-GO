package runtimecfg

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestApplyPatchRejectsUnknownTypesAndBounds(t *testing.T) {
	tests := []struct {
		name  string
		patch string
		want  string
	}{
		{name: "unknown", patch: `{"mystery":true}`, want: "not a recognized setting"},
		{name: "wrong type", patch: `{"max_concurrency":"32"}`, want: "JSON integer"},
		{name: "fraction", patch: `{"request_timeout_seconds":1.5}`, want: "JSON integer"},
		{name: "too small", patch: `{"max_request_bytes":1024}`, want: "between"},
		{name: "concurrency too large", patch: `{"max_concurrency":1000001}`, want: "between 1 and 1000000"},
		{name: "invalid origin", patch: `{"cors_origins":"https://ok.test/path"}`, want: "invalid origin"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var patch map[string]json.RawMessage
			if err := json.Unmarshal([]byte(test.patch), &patch); err != nil {
				t.Fatal(err)
			}
			if _, _, err := ApplyPatch(Defaults(), patch); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ApplyPatch() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestApplyPatchAcceptsMillionRequestConcurrency(t *testing.T) {
	var patch map[string]json.RawMessage
	if err := json.Unmarshal([]byte(`{"max_concurrency":1000000}`), &patch); err != nil {
		t.Fatal(err)
	}
	configured, changed, err := ApplyPatch(Defaults(), patch)
	if err != nil {
		t.Fatal(err)
	}
	if configured.MaxConcurrency != maxGlobalConcurrency || changed["max_concurrency"] != maxGlobalConcurrency {
		t.Fatalf("max concurrency patch = configured:%d changed:%v", configured.MaxConcurrency, changed["max_concurrency"])
	}
}

func TestRuntimeAppliesHotSettingsAndReportsRestart(t *testing.T) {
	defaults := Defaults()
	active := defaults
	active.PublicBaseURL = "https://old.test"
	runtime := MustRuntime(defaults, active)
	configured := active
	configured.PublicBaseURL = "https://new.test"
	configured.RequestTimeoutSeconds = 45
	configured.CORSOrigins = "https://console.test"
	runtime.Apply(configured)

	if got := runtime.Active(); got.RequestTimeoutSeconds != 45 || got.CORSOrigins != "https://console.test" {
		t.Fatalf("active hot settings = %+v", got)
	} else if got.PublicBaseURL != "https://old.test" {
		t.Fatalf("restart-only public URL changed to %q", got.PublicBaseURL)
	}
	if got := RestartRequired(configured, runtime.Active()); !reflect.DeepEqual(got, []string{"public_base_url"}) {
		t.Fatalf("RestartRequired() = %v", got)
	}
	select {
	case <-runtime.Changes():
	default:
		t.Fatal("runtime change notification was not emitted")
	}
}

func TestResolveFillsDefaultsAndNormalizesOrigins(t *testing.T) {
	defaults := Defaults()
	defaults.PublicBaseURL = "http://127.0.0.1:18080"
	resolved, err := Resolve(defaults, map[string]any{
		"request_timeout_seconds": float64(30),
		"cors_origins":            "https://b.test/, https://a.test, https://a.test",
		"legacy_key":              true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.MaxRequestBytes != 32<<20 || resolved.CORSOrigins != "https://a.test, https://b.test" {
		t.Fatalf("Resolve() = %+v", resolved)
	}
}

func TestResolveTreatsLegacyEmptyPublicURLAsEnvironmentDefault(t *testing.T) {
	defaults := Defaults()
	defaults.PublicBaseURL = "http://127.0.0.1:18080"
	resolved, err := Resolve(defaults, map[string]any{"public_base_url": ""})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.PublicBaseURL != defaults.PublicBaseURL {
		t.Fatalf("public URL = %q, want %q", resolved.PublicBaseURL, defaults.PublicBaseURL)
	}
}
