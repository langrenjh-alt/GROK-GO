package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/langrenjh-alt/GROK-GO/internal/admin"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/store"
)

func (h *Handler) mountKeys(router chi.Router) {
	router.Get("/keys", h.listKeys)
	router.Post("/keys", h.createKey)
	router.Get("/keys/{id}", h.getKey)
	router.Patch("/keys/{id}", h.updateKey)
	router.Put("/keys/{id}", h.updateKey)
	router.Post("/keys/{id}/reveal", h.revealKey)
	router.Delete("/keys/{id}", h.deleteKey)
}

func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	page := pagination(r)
	items, err := h.config.Management.ListClientKeys(r.Context(), page)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	total, err := h.config.Management.CountClientKeys(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, listEnvelope(items, page.Limit, page.Offset, total))
}

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	var request struct {
		Name              string `json:"name"`
		RPM               int    `json:"rpm"`
		ConcurrencyLimit  int    `json:"concurrency_limit"`
		DailyRequestLimit int64  `json:"daily_request_limit"`
		MonthlyTokenLimit int64  `json:"monthly_token_limit"`
		ExpiresAt         string `json:"expires_at"`
	}
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	expiresAt, err := parseFlexibleTime(request.ExpiresAt)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_expiration", "expires_at must be an RFC3339 or local date-time value.")
		return
	}
	issued, err := h.config.Management.CreateClientKey(r.Context(), admin.CreateClientKeyInput{Name: request.Name, RPM: request.RPM, ConcurrencyLimit: request.ConcurrencyLimit, DailyRequestLimit: request.DailyRequestLimit, MonthlyTokenLimit: request.MonthlyTokenLimit, ExpiresAt: expiresAt})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusCreated, map[string]any{"key": issued.Plaintext, "record": issued.Key})
}

func (h *Handler) getKey(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	item, err := h.config.Management.GetClientKey(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, item)
}

func (h *Handler) updateKey(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	item, err := h.config.Management.GetClientKey(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	var request struct {
		Name              *string     `json:"name"`
		Enabled           *bool       `json:"enabled"`
		RPM               *int        `json:"rpm"`
		ConcurrencyLimit  *int        `json:"concurrency_limit"`
		DailyRequestLimit *int64      `json:"daily_request_limit"`
		MonthlyTokenLimit *int64      `json:"monthly_token_limit"`
		ExpiresAt         **time.Time `json:"expires_at"`
	}
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if request.Name != nil {
		item.Name = strings.TrimSpace(*request.Name)
	}
	if request.Enabled != nil {
		item.Enabled = *request.Enabled
	}
	if request.RPM != nil {
		item.RPM = *request.RPM
	}
	if request.ConcurrencyLimit != nil {
		item.ConcurrencyLimit = *request.ConcurrencyLimit
	}
	if request.DailyRequestLimit != nil {
		item.DailyRequestLimit = *request.DailyRequestLimit
	}
	if request.MonthlyTokenLimit != nil {
		item.MonthlyTokenLimit = *request.MonthlyTokenLimit
	}
	if request.ExpiresAt != nil {
		item.ExpiresAt = *request.ExpiresAt
	}
	if item.Name == "" || item.RPM < 0 || item.ConcurrencyLimit < 0 || item.DailyRequestLimit < 0 || item.MonthlyTokenLimit < 0 {
		writeAPIError(w, http.StatusBadRequest, "invalid_key", "Client key fields are invalid.")
		return
	}
	if err := h.config.Management.UpdateClientKey(r.Context(), item); err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, item)
}

func (h *Handler) deleteKey(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	if err := h.config.Management.DeleteClientKey(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"deleted": true})
}

func (h *Handler) revealKey(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	plaintext, err := h.config.Management.RevealClientKey(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, admin.ErrClientKeySecretUnavailable) {
		writeAPIError(w, http.StatusConflict, "key_secret_unavailable", "This key predates encrypted secret storage and must be replaced.")
		return
	}
	if err != nil {
		writeServiceError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeData(w, http.StatusOK, map[string]string{"key": plaintext})
}

func (h *Handler) mountModels(router chi.Router) {
	router.Get("/models", h.listModels)
	router.Post("/models", h.createModel)
	router.Get("/models/{id}", h.getModel)
	router.Patch("/models/{id}", h.updateModel)
	router.Put("/models/{id}", h.updateModel)
	router.Delete("/models/{id}", h.deleteModel)
}

func (h *Handler) listModels(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	page := pagination(r)
	filter := store.ModelFilter{Pagination: page, Capability: domain.Capability(r.URL.Query().Get("capability")), Query: r.URL.Query().Get("q")}
	if enabled := r.URL.Query().Get("enabled"); enabled != "" {
		value, err := strconv.ParseBool(enabled)
		if err == nil {
			filter.Enabled = &value
		}
	}
	items, err := h.config.Management.ListModels(r.Context(), filter)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	total, err := h.config.Management.CountModels(r.Context(), filter)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, listEnvelope(items, page.Limit, page.Offset, total))
}

func (h *Handler) createModel(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	var model domain.ModelSpec
	if err := h.decodeJSON(w, r, &model); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if err := h.config.Management.CreateModel(r.Context(), &model); err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusCreated, model)
}

func (h *Handler) getModel(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	item, err := h.config.Management.GetModel(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, item)
}

func (h *Handler) updateModel(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	model, err := h.config.Management.GetModel(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	var request struct {
		UpstreamModel   *string                  `json:"upstream_model"`
		DisplayName     *string                  `json:"display_name"`
		Capability      *domain.Capability       `json:"capability"`
		CredentialKinds *[]domain.CredentialKind `json:"credential_kinds"`
		MinimumTier     *string                  `json:"minimum_tier"`
		Aliases         *[]string                `json:"aliases"`
		PreferBest      *bool                    `json:"prefer_best"`
		Enabled         *bool                    `json:"enabled"`
	}
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if request.UpstreamModel != nil {
		model.UpstreamModel = *request.UpstreamModel
	}
	if request.DisplayName != nil {
		model.DisplayName = *request.DisplayName
	}
	if request.Capability != nil {
		model.Capability = *request.Capability
	}
	if request.CredentialKinds != nil {
		model.CredentialKinds = *request.CredentialKinds
	}
	if request.MinimumTier != nil {
		model.MinimumTier = *request.MinimumTier
	}
	if request.Aliases != nil {
		model.Aliases = *request.Aliases
	}
	if request.PreferBest != nil {
		model.PreferBest = *request.PreferBest
	}
	if request.Enabled != nil {
		model.Enabled = *request.Enabled
	}
	if err := h.config.Management.UpdateModel(r.Context(), model); err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, model)
}

func (h *Handler) deleteModel(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	if err := h.config.Management.DeleteModel(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"deleted": true})
}

func (h *Handler) mountProxies(router chi.Router) {
	router.Get("/proxies", h.listProxies)
	router.Post("/proxies", h.createProxy)
	router.Get("/proxies/{id}", h.getProxy)
	router.Patch("/proxies/{id}", h.updateProxy)
	router.Put("/proxies/{id}", h.updateProxy)
	router.Delete("/proxies/{id}", h.deleteProxy)
	router.Post("/proxies/{id}/check", h.checkProxy)
}

func (h *Handler) listProxies(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	page := pagination(r)
	items, err := h.config.Management.ListProxies(r.Context(), page)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	total, err := h.config.Management.CountProxies(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, listEnvelope(items, page.Limit, page.Offset, total))
}

func (h *Handler) createProxy(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	var request admin.CreateProxyInput
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	item, err := h.config.Management.CreateProxy(r.Context(), request)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusCreated, item)
}

func (h *Handler) getProxy(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	item, err := h.config.Management.GetProxy(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, item)
}

func (h *Handler) updateProxy(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	var request admin.UpdateProxyInput
	if err := h.decodeJSON(w, r, &request); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	item, err := h.config.Management.UpdateProxy(r.Context(), chi.URLParam(r, "id"), request)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, item)
}

func (h *Handler) deleteProxy(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	if err := h.config.Management.DeleteProxy(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"deleted": true})
}

func (h *Handler) checkProxy(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	if h.config.ProxyChecker == nil {
		writeAPIError(w, http.StatusNotImplemented, "proxy_check_unavailable", "Proxy checking is not configured.")
		return
	}
	id := chi.URLParam(r, "id")
	proxyURL, err := h.config.Management.GetProxyURL(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	now := time.Now().UTC()
	checkedAt := &now
	healthy := true
	lastError := ""
	if err = h.config.ProxyChecker.CheckProxy(r.Context(), proxyURL); err != nil {
		healthy, lastError = false, err.Error()
	}
	item, updateErr := h.config.Management.UpdateProxy(r.Context(), id, admin.UpdateProxyInput{Healthy: &healthy, LastCheckedAt: &checkedAt, LastError: &lastError})
	if updateErr != nil {
		writeServiceError(w, updateErr)
		return
	}
	writeData(w, http.StatusOK, item)
}

func (h *Handler) mountLogs(router chi.Router) {
	router.Get("/logs", h.listLogs)
	router.Get("/logs/{id}", h.getLog)
	router.Delete("/logs/{id}", h.deleteLog)
	router.Delete("/logs", h.deleteOldLogs)
}

func (h *Handler) listLogs(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	page := pagination(r)
	filter := store.RequestLogFilter{Pagination: page, Query: r.URL.Query().Get("q"), RequestID: r.URL.Query().Get("request_id"), ClientKeyID: r.URL.Query().Get("client_key_id"), AccountID: r.URL.Query().Get("account_id"), Model: r.URL.Query().Get("model"), Endpoint: r.URL.Query().Get("endpoint"), StatusCode: queryInt(r, "status_code", 0)}
	if statusClass := strings.TrimSpace(r.URL.Query().Get("status_class")); statusClass != "" {
		switch statusClass {
		case "2xx":
			filter.StatusMin, filter.StatusMax = 200, 299
		case "3xx":
			filter.StatusMin, filter.StatusMax = 300, 399
		case "4xx":
			filter.StatusMin, filter.StatusMax = 400, 499
		case "5xx":
			filter.StatusMin, filter.StatusMax = 500, 599
		default:
			writeAPIError(w, http.StatusBadRequest, "invalid_status_class", "status_class must be 2xx, 3xx, 4xx, or 5xx.")
			return
		}
	}
	filter.CreatedFrom = queryTime(r, "created_from")
	filter.CreatedTo = queryTime(r, "created_to")
	items, err := h.config.Management.ListRequestLogs(r.Context(), filter)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	total, err := h.config.Management.CountRequestLogs(r.Context(), filter)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, listEnvelope(items, page.Limit, page.Offset, total))
}

func (h *Handler) getLog(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	item, err := h.config.Management.GetRequestLog(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, item)
}

func (h *Handler) deleteLog(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	if err := h.config.Management.DeleteRequestLog(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"deleted": true})
}

func (h *Handler) deleteOldLogs(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	before := queryTime(r, "before")
	if before == nil {
		writeAPIError(w, http.StatusBadRequest, "missing_before", "The before timestamp is required.")
		return
	}
	deleted, err := h.config.Management.DeleteRequestLogsBefore(r.Context(), *before)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, map[string]any{"deleted": deleted})
}

func queryInt(r *http.Request, key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get(key)))
	if err != nil {
		return fallback
	}
	return value
}

func queryTime(r *http.Request, key string) *time.Time {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil
	}
	return &parsed
}

func parseFlexibleTime(value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04", "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return &parsed, nil
		}
	}
	return nil, fmt.Errorf("invalid time")
}
