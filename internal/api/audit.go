package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/store"
)

const auditWriteTimeout = 2 * time.Second

type auditResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *auditResponseWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *auditResponseWriter) Write(content []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(content)
}

func (w *auditResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (h *Handler) auditMutations(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.config.Audit == nil || !isMutationMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		wrapped := &auditResponseWriter{ResponseWriter: w}
		next.ServeHTTP(wrapped, r)
		status := wrapped.status
		if status == 0 {
			status = http.StatusOK
		}
		principal := principalFromContext(r.Context())
		if principal == nil || strings.TrimSpace(principal.ID) == "" {
			return
		}
		action, resourceType, resourceID := classifyAuditMutation(r.Method, r.URL.Path, principal.ID)
		route := ""
		if routeContext := chi.RouteContext(r.Context()); routeContext != nil {
			route = routeContext.RoutePattern()
		}
		if route == "" {
			route = "/" + strings.Trim(strings.Split(strings.Trim(r.URL.Path, "/"), "/")[0], " ")
		}
		metadata, _ := json.Marshal(map[string]any{
			"method":      r.Method,
			"route":       truncateAuditValue(route, 160),
			"status":      status,
			"success":     status >= 200 && status < 400,
			"duration_ms": max(0, time.Since(started).Milliseconds()),
		})
		entry := &domain.AuditLog{
			AdminID: principal.ID, Action: action, ResourceType: resourceType,
			ResourceID: resourceID, IPAddress: truncateAuditValue(h.clientIP(r), 128),
			Metadata: metadata, CreatedAt: time.Now().UTC(),
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), auditWriteTimeout)
		defer cancel()
		_ = h.config.Audit.CreateAuditLog(ctx, entry)
	})
}

func isMutationMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

func classifyAuditMutation(method, path, principalID string) (action, resourceType, resourceID string) {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) == 0 || segments[0] == "" {
		return "admin.mutate", "admin", safeAuditID(principalID)
	}
	base := strings.ToLower(segments[0])
	resourceTypes := map[string]string{
		"accounts": "account", "keys": "api_key", "models": "model", "proxies": "proxy",
		"logs": "request_log", "audit-logs": "audit_log", "media": "media", "settings": "settings",
		"auth": "admin_security", "oauth": "oauth", "debugger": "debugger", "tokens": "account",
	}
	resourceType = resourceTypes[base]
	if resourceType == "" {
		resourceType = strings.TrimSuffix(base, "s")
	}
	reserved := map[string]bool{
		"batch": true, "batch-delete": true, "cleanup": true, "import": true, "policy": true,
		"probe": true, "quota-summary": true, "build-oauth": true, "add": true, "export": true,
	}
	operation := ""
	if len(segments) > 1 {
		second := strings.ToLower(segments[1])
		if reserved[second] || base == "auth" || base == "oauth" || base == "settings" || base == "debugger" {
			operation = second
		} else {
			resourceID = safeAuditID(segments[1])
			if len(segments) > 2 {
				operation = strings.ToLower(segments[2])
			}
		}
	}
	verb := "mutate"
	switch method {
	case http.MethodPost:
		verb = "create"
	case http.MethodPut, http.MethodPatch:
		verb = "update"
	case http.MethodDelete:
		verb = "delete"
	}
	operation = safeAuditID(strings.ReplaceAll(operation, "-", "_"))
	if operation != "" {
		verb = operation
		if (method == http.MethodPut || method == http.MethodPatch) && operation == "policy" {
			verb = "update"
			resourceType = "account_policy"
		}
		if method == http.MethodDelete && operation == "totp" {
			verb = "disable_totp"
		}
	}
	if base == "logs" && method == http.MethodDelete && resourceID == "" {
		verb = "cleanup"
	}
	if base == "audit-logs" && method == http.MethodDelete {
		verb = "cleanup"
	}
	if base == "debugger" {
		verb = "execute"
	}
	if base == "settings" {
		verb = "update"
	}
	if base == "auth" {
		resourceID = safeAuditID(principalID)
		if len(segments) > 2 {
			verb = strings.ReplaceAll(strings.Join(segments[1:], "_"), "-", "_")
		}
	}
	return resourceType + "." + verb, resourceType, resourceID
}

func safeAuditID(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 128 {
		return ""
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' {
			continue
		}
		return ""
	}
	return value
}

func truncateAuditValue(value string, maximum int) string {
	value = strings.TrimSpace(value)
	if len(value) <= maximum {
		return value
	}
	return value[:maximum]
}

func (h *Handler) mountAuditLogs(router chi.Router) {
	router.Get("/audit-logs", h.listAuditLogs)
	router.Delete("/audit-logs", h.deleteOldAuditLogs)
}

func (h *Handler) listAuditLogs(w http.ResponseWriter, r *http.Request) {
	if h.config.Audit == nil {
		writeData(w, http.StatusOK, listEnvelope([]domain.AuditLog{}, 50, 0))
		return
	}
	page := pagination(r)
	filter := store.AuditLogFilter{
		Pagination: page, AdminID: r.URL.Query().Get("admin_id"), Action: r.URL.Query().Get("action"),
		ResourceType: r.URL.Query().Get("resource_type"), ResourceID: r.URL.Query().Get("resource_id"),
		Query: r.URL.Query().Get("q"), StatusCode: queryInt(r, "status_code", 0),
		CreatedFrom: queryTime(r, "created_from"), CreatedTo: queryTime(r, "created_to"),
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("success")); raw != "" {
		value, err := strconv.ParseBool(raw)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, "invalid_audit_filter", "success must be true or false.")
			return
		}
		filter.Success = &value
	}
	items, err := h.config.Audit.ListAuditLogs(r.Context(), filter)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	total, err := h.config.Audit.CountAuditLogs(r.Context(), filter)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeData(w, http.StatusOK, listEnvelope(items, page.Limit, page.Offset, total))
}

func (h *Handler) deleteOldAuditLogs(w http.ResponseWriter, r *http.Request) {
	if h.config.Audit == nil {
		writeAPIError(w, http.StatusNotImplemented, "audit_unavailable", "Audit logging is not configured.")
		return
	}
	before := queryTime(r, "before")
	if before == nil {
		writeAPIError(w, http.StatusBadRequest, "missing_before", "The before timestamp is required.")
		return
	}
	deleted, err := h.config.Audit.DeleteAuditLogsBefore(r.Context(), *before)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeData(w, http.StatusOK, map[string]any{"deleted": deleted})
}
