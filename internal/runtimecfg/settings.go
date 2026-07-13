package runtimecfg

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
)

const (
	minRequestBytes = 1 << 20
	maxRequestBytes = 1 << 30
)

// Values is the complete, validated service-settings snapshot. Keep this type
// aligned with the administrator settings form; settings without runtime
// behavior do not belong here.
type Values struct {
	PublicBaseURL         string `json:"public_base_url"`
	RequestTimeoutSeconds int    `json:"request_timeout_seconds"`
	MaxRequestBytes       int64  `json:"max_request_bytes"`
	MaxConcurrency        int    `json:"max_concurrency"`
	LogRetentionDays      int    `json:"log_retention_days"`
	CORSOrigins           string `json:"cors_origins"`
	TrustProxyHeaders     bool   `json:"trust_proxy_headers"`
}

func Defaults() Values {
	return Values{
		PublicBaseURL:         "http://127.0.0.1:8080",
		RequestTimeoutSeconds: 120,
		MaxRequestBytes:       32 << 20,
		MaxConcurrency:        32,
		LogRetentionDays:      30,
	}
}

func (v Values) Map() map[string]any {
	return map[string]any{
		"public_base_url":         v.PublicBaseURL,
		"request_timeout_seconds": v.RequestTimeoutSeconds,
		"max_request_bytes":       v.MaxRequestBytes,
		"max_concurrency":         v.MaxConcurrency,
		"log_retention_days":      v.LogRetentionDays,
		"cors_origins":            v.CORSOrigins,
		"trust_proxy_headers":     v.TrustProxyHeaders,
	}
}

func (v Values) Validate() error {
	if err := validateBaseURL(v.PublicBaseURL); err != nil {
		return fieldError("public_base_url", err)
	}
	if v.RequestTimeoutSeconds < 1 || v.RequestTimeoutSeconds > 3600 {
		return fieldError("request_timeout_seconds", errors.New("must be between 1 and 3600"))
	}
	if v.MaxRequestBytes < minRequestBytes || v.MaxRequestBytes > maxRequestBytes {
		return fieldError("max_request_bytes", fmt.Errorf("must be between %d and %d", minRequestBytes, maxRequestBytes))
	}
	if v.MaxConcurrency < 1 || v.MaxConcurrency > 10_000 {
		return fieldError("max_concurrency", errors.New("must be between 1 and 10000"))
	}
	if v.LogRetentionDays < 1 || v.LogRetentionDays > 3650 {
		return fieldError("log_retention_days", errors.New("must be between 1 and 3650"))
	}
	if _, err := NormalizeCORSOrigins(v.CORSOrigins); err != nil {
		return fieldError("cors_origins", err)
	}
	return nil
}

// Resolve overlays recognized persisted values on defaults. Unknown legacy
// database keys are ignored here; the HTTP patch decoder rejects them.
func Resolve(defaults Values, persisted map[string]any) (Values, error) {
	if err := defaults.Validate(); err != nil {
		return Values{}, fmt.Errorf("invalid setting defaults: %w", err)
	}
	result := defaults
	for key := range result.Map() {
		value, ok := persisted[key]
		if !ok {
			continue
		}
		// Older service settings stored an empty public URL to mean "use the
		// environment value". Preserve that upgrade behavior while keeping new
		// administrator patches strict.
		if key == "public_base_url" {
			if text, isString := value.(string); isString && strings.TrimSpace(text) == "" {
				continue
			}
		}
		if err := assignPersisted(&result, key, value); err != nil {
			return Values{}, err
		}
	}
	normalized, err := NormalizeCORSOrigins(result.CORSOrigins)
	if err != nil {
		return Values{}, fieldError("cors_origins", err)
	}
	result.CORSOrigins = normalized
	if err := result.Validate(); err != nil {
		return Values{}, err
	}
	return result, nil
}

// ApplyPatch performs strict JSON-type checking and rejects unknown fields.
// It returns both the complete next snapshot and a normalized persistence
// patch, so stores can merge without a read/modify/write race.
func ApplyPatch(current Values, patch map[string]json.RawMessage) (Values, map[string]any, error) {
	if len(patch) == 0 {
		return Values{}, nil, errors.New("settings patch must not be empty")
	}
	result := current
	changed := make(map[string]any, len(patch))
	for key, raw := range patch {
		if _, ok := current.Map()[key]; !ok {
			return Values{}, nil, fieldError(key, errors.New("is not a recognized setting"))
		}
		if err := assignJSON(&result, key, raw); err != nil {
			return Values{}, nil, err
		}
	}
	normalized, err := NormalizeCORSOrigins(result.CORSOrigins)
	if err != nil {
		return Values{}, nil, fieldError("cors_origins", err)
	}
	result.CORSOrigins = normalized
	if err := result.Validate(); err != nil {
		return Values{}, nil, err
	}
	all := result.Map()
	for key := range patch {
		changed[key] = all[key]
	}
	return result, changed, nil
}

func Envelope(configured, defaults, active Values) map[string]any {
	result := configured.Map()
	result["defaults"] = defaults.Map()
	result["active"] = active.Map()
	result["restart_required"] = RestartRequired(configured, active)
	return result
}

var restartFields = []string{"public_base_url", "trust_proxy_headers"}

func RestartRequired(configured, active Values) []string {
	configuredMap, activeMap := configured.Map(), active.Map()
	result := make([]string, 0, len(restartFields))
	for _, key := range restartFields {
		if fmt.Sprint(configuredMap[key]) != fmt.Sprint(activeMap[key]) {
			result = append(result, key)
		}
	}
	return result
}

// Runtime owns the process-effective settings. Apply activates only fields
// that are safe to change while requests are in flight.
type Runtime struct {
	defaults Values
	active   atomic.Pointer[Values]
	changed  chan struct{}
}

func NewRuntime(defaults, active Values) (*Runtime, error) {
	if err := defaults.Validate(); err != nil {
		return nil, fmt.Errorf("invalid setting defaults: %w", err)
	}
	if err := active.Validate(); err != nil {
		return nil, fmt.Errorf("invalid active settings: %w", err)
	}
	runtime := &Runtime{defaults: defaults, changed: make(chan struct{}, 1)}
	runtime.active.Store(&active)
	return runtime, nil
}

func MustRuntime(defaults, active Values) *Runtime {
	runtime, err := NewRuntime(defaults, active)
	if err != nil {
		panic(err)
	}
	return runtime
}

func (r *Runtime) Defaults() Values {
	if r == nil {
		return Defaults()
	}
	return r.defaults
}

func (r *Runtime) Active() Values {
	if r == nil || r.active.Load() == nil {
		return Defaults()
	}
	return *r.active.Load()
}

func (r *Runtime) Apply(configured Values) {
	current := r.Active()
	next := current
	next.RequestTimeoutSeconds = configured.RequestTimeoutSeconds
	next.MaxRequestBytes = configured.MaxRequestBytes
	next.MaxConcurrency = configured.MaxConcurrency
	next.LogRetentionDays = configured.LogRetentionDays
	next.CORSOrigins = configured.CORSOrigins
	r.active.Store(&next)
	select {
	case r.changed <- struct{}{}:
	default:
	}
}

func (r *Runtime) Changes() <-chan struct{} {
	if r == nil {
		return nil
	}
	return r.changed
}

func NormalizeCORSOrigins(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	if len(raw) > 8192 {
		return "", errors.New("is too long")
	}
	seen := make(map[string]struct{})
	origins := make([]string, 0)
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimRight(strings.TrimSpace(item), "/")
		if item == "" {
			return "", errors.New("contains an empty origin")
		}
		if item != "*" {
			parsed, err := url.Parse(item)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
				return "", fmt.Errorf("contains invalid origin %q", item)
			}
			item = parsed.Scheme + "://" + parsed.Host
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		origins = append(origins, item)
	}
	if len(origins) > 64 {
		return "", errors.New("contains more than 64 origins")
	}
	if _, wildcard := seen["*"]; wildcard && len(origins) != 1 {
		return "", errors.New("cannot combine * with explicit origins")
	}
	sort.Strings(origins)
	return strings.Join(origins, ", "), nil
}

func assignJSON(destination *Values, key string, raw json.RawMessage) error {
	switch key {
	case "public_base_url", "cors_origins":
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return fieldTypeError(key, "string")
		}
		assignString(destination, key, value)
	case "request_timeout_seconds", "max_concurrency", "log_retention_days":
		value, err := strictInteger(raw)
		if err != nil || int64(int(value)) != value {
			return fieldTypeError(key, "integer")
		}
		assignInteger(destination, key, value)
	case "max_request_bytes":
		value, err := strictInteger(raw)
		if err != nil {
			return fieldTypeError(key, "integer")
		}
		destination.MaxRequestBytes = value
	case "trust_proxy_headers":
		var value bool
		if err := json.Unmarshal(raw, &value); err != nil {
			return fieldTypeError(key, "boolean")
		}
		destination.TrustProxyHeaders = value
	}
	return nil
}

func assignPersisted(destination *Values, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fieldError(key, errors.New("could not be decoded"))
	}
	if err := assignJSON(destination, key, encoded); err != nil {
		return fmt.Errorf("invalid persisted setting: %w", err)
	}
	return nil
}

func assignString(destination *Values, key, value string) {
	switch key {
	case "public_base_url":
		destination.PublicBaseURL = strings.TrimRight(strings.TrimSpace(value), "/")
	case "cors_origins":
		destination.CORSOrigins = value
	}
}

func assignInteger(destination *Values, key string, value int64) {
	switch key {
	case "request_timeout_seconds":
		destination.RequestTimeoutSeconds = int(value)
	case "max_concurrency":
		destination.MaxConcurrency = int(value)
	case "log_retention_days":
		destination.LogRetentionDays = int(value)
	}
}

func strictInteger(raw json.RawMessage) (int64, error) {
	text := strings.TrimSpace(string(raw))
	if text == "" || strings.ContainsAny(text, ".eE") {
		return 0, errors.New("not an integer")
	}
	return strconv.ParseInt(text, 10, 64)
}

func validateBaseURL(raw string) error {
	if raw == "" {
		return errors.New("is required")
	}
	if len(raw) > 2048 {
		return errors.New("is too long")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("must be an absolute HTTP or HTTPS URL without credentials, query, or fragment")
	}
	return nil
}

func fieldError(field string, err error) error {
	return fmt.Errorf("%s: %w", field, err)
}

func fieldTypeError(field, expected string) error {
	return fieldError(field, fmt.Errorf("must be a JSON %s", expected))
}
