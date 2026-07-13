package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/langrenjh-alt/GROK-GO/internal/configevent"
	"github.com/langrenjh-alt/GROK-GO/internal/runtimecfg"
)

func (h *Handler) mountExtras(router chi.Router) {
	router.Get("/settings", h.getSettings)
	router.Put("/settings", h.saveSettings)
	router.Patch("/settings", h.saveSettings)
	router.Get("/media", h.listMedia)
	router.Get("/media/summary", h.mediaSummary)
	router.Post("/media/batch-delete", h.batchDeleteMedia)
	router.Post("/media/cleanup", h.cleanupMedia)
	router.Delete("/media/{id}", h.deleteMedia)
	router.Post("/debugger", h.debugger)
}

func (h *Handler) getSettings(w http.ResponseWriter, r *http.Request) {
	if h.config.Settings == nil {
		defaults := h.config.RuntimeSettings.Defaults()
		writeData(w, http.StatusOK, runtimecfg.Envelope(defaults, defaults, h.config.RuntimeSettings.Active()))
		return
	}
	persisted, err := h.config.Settings.LoadSettings(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	configured, err := runtimecfg.Resolve(h.config.RuntimeSettings.Defaults(), persisted)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "invalid_persisted_settings", err.Error())
		return
	}
	writeData(w, http.StatusOK, runtimecfg.Envelope(configured, h.config.RuntimeSettings.Defaults(), h.config.RuntimeSettings.Active()))
}

func (h *Handler) saveSettings(w http.ResponseWriter, r *http.Request) {
	if h.config.Settings == nil {
		writeAPIError(w, http.StatusNotImplemented, "settings_unavailable", "Runtime settings are not configured.")
		return
	}
	var patch map[string]json.RawMessage
	if err := h.decodeJSON(w, r, &patch); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	persisted, err := h.config.Settings.LoadSettings(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	current, err := runtimecfg.Resolve(h.config.RuntimeSettings.Defaults(), persisted)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "invalid_persisted_settings", err.Error())
		return
	}
	configured, normalizedPatch, err := runtimecfg.ApplyPatch(current, patch)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_settings", err.Error())
		return
	}
	if err := h.config.Settings.SaveSettings(r.Context(), normalizedPatch); err != nil {
		writeServiceError(w, err)
		return
	}
	h.config.RuntimeSettings.Apply(configured)
	h.notifyConfiguration(r.Context(), configevent.ScopeRuntimeSettings)
	writeData(w, http.StatusOK, runtimecfg.Envelope(configured, h.config.RuntimeSettings.Defaults(), h.config.RuntimeSettings.Active()))
}

func (h *Handler) listMedia(w http.ResponseWriter, r *http.Request) {
	if h.config.Media == nil {
		writeData(w, http.StatusOK, listEnvelope([]any{}, 50, 0))
		return
	}
	page := pagination(r)
	items, total, err := h.config.Media.ListMedia(r.Context(), page)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, listEnvelope(items, page.Limit, page.Offset, total))
}

func (h *Handler) deleteMedia(w http.ResponseWriter, r *http.Request) {
	if h.config.Media == nil {
		writeAPIError(w, http.StatusNotImplemented, "media_unavailable", "Media administration is not configured.")
		return
	}
	if err := h.config.Media.DeleteMedia(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"deleted": true})
}

func (h *Handler) mediaSummary(w http.ResponseWriter, r *http.Request) {
	if h.config.Media == nil {
		writeAPIError(w, http.StatusNotImplemented, "media_unavailable", "Media administration is not configured.")
		return
	}
	result, err := h.config.Media.MediaSummary(r.Context(), 24*time.Hour)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeData(w, http.StatusOK, result)
}

func (h *Handler) batchDeleteMedia(w http.ResponseWriter, r *http.Request) {
	if h.config.Media == nil {
		writeAPIError(w, http.StatusNotImplemented, "media_unavailable", "Media administration is not configured.")
		return
	}
	var request struct {
		IDs []string `json:"ids"`
	}
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	request.IDs = uniqueStrings(request.IDs)
	if len(request.IDs) == 0 || len(request.IDs) > 500 {
		writeAPIError(w, http.StatusBadRequest, "invalid_media_batch", "Select between 1 and 500 media objects.")
		return
	}
	result, err := h.config.Media.DeleteMediaBatch(r.Context(), request.IDs)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeData(w, http.StatusOK, result)
}

func (h *Handler) cleanupMedia(w http.ResponseWriter, r *http.Request) {
	if h.config.Media == nil {
		writeAPIError(w, http.StatusNotImplemented, "media_unavailable", "Media administration is not configured.")
		return
	}
	var request struct {
		Mode    string `json:"mode"`
		Confirm bool   `json:"confirm"`
	}
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	mode := strings.ToLower(strings.TrimSpace(request.Mode))
	if mode != "expired" && mode != "all" {
		writeAPIError(w, http.StatusBadRequest, "invalid_cleanup_mode", "Cleanup mode must be expired or all.")
		return
	}
	if mode == "all" && !request.Confirm {
		writeAPIError(w, http.StatusBadRequest, "cleanup_confirmation_required", "Clearing all media requires confirmation.")
		return
	}
	result, err := h.config.Media.CleanupMedia(r.Context(), mode == "all")
	if err != nil {
		writeServiceError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeData(w, http.StatusOK, result)
}

func (h *Handler) debugger(w http.ResponseWriter, r *http.Request) {
	if h.config.Gateway == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "gateway_unavailable", "Gateway handler is not configured.")
		return
	}
	var request struct {
		Method   string          `json:"method"`
		Endpoint string          `json:"endpoint"`
		APIKey   string          `json:"api_key"`
		Body     json.RawMessage `json:"body"`
	}
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	method := strings.ToUpper(strings.TrimSpace(request.Method))
	if method == "" {
		method = http.MethodPost
	}
	if method != http.MethodGet && method != http.MethodPost {
		writeAPIError(w, http.StatusBadRequest, "invalid_debug_method", "Debugger method must be GET or POST.")
		return
	}
	parsed, err := url.ParseRequestURI(request.Endpoint)
	if err != nil || !strings.HasPrefix(parsed.Path, "/v1/") || parsed.IsAbs() || parsed.Host != "" {
		writeAPIError(w, http.StatusBadRequest, "invalid_debug_endpoint", "Debugger endpoint must be a local /v1 path.")
		return
	}
	debugRequest, err := http.NewRequestWithContext(r.Context(), method, "http://gateway.local"+parsed.RequestURI(), bytes.NewReader(request.Body))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	debugRequest.Header.Set("Content-Type", "application/json")
	if request.APIKey != "" {
		debugRequest.Header.Set("Authorization", "Bearer "+request.APIKey)
	}
	recorder := httptest.NewRecorder()
	h.config.Gateway.ServeHTTP(recorder, debugRequest)
	body, err := io.ReadAll(io.LimitReader(recorder.Result().Body, 4<<20))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	var decoded any
	if json.Unmarshal(body, &decoded) != nil {
		decoded = string(body)
	}
	writeData(w, http.StatusOK, map[string]any{"status": recorder.Code, "headers": safeResponseHeaders(recorder.Header()), "body": decoded})
}

func safeResponseHeaders(headers http.Header) http.Header {
	result := make(http.Header)
	for _, name := range []string{"Content-Type", "X-Request-ID", "Cache-Control"} {
		if value := headers.Values(name); len(value) > 0 {
			result[name] = append([]string(nil), value...)
		}
	}
	return result
}

type MemorySettingsStore struct {
	mu     sync.RWMutex
	values map[string]any
}

func NewMemorySettingsStore(initial map[string]any) *MemorySettingsStore {
	store := &MemorySettingsStore{}
	_ = store.SaveSettings(context.Background(), initial)
	return store
}

func (s *MemorySettingsStore) LoadSettings(context.Context) (map[string]any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneMap(s.values), nil
}

func (s *MemorySettingsStore) SaveSettings(_ context.Context, values map[string]any) error {
	if values == nil {
		values = map[string]any{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = make(map[string]any)
	}
	for key, value := range cloneMap(values) {
		s.values[key] = value
	}
	return nil
}

func cloneMap(value map[string]any) map[string]any {
	encoded, _ := json.Marshal(value)
	var result map[string]any
	_ = json.Unmarshal(encoded, &result)
	if result == nil {
		result = map[string]any{}
	}
	return result
}
