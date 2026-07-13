package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/store"
)

type memoryAuditRepository struct {
	mu         sync.Mutex
	logs       []domain.AuditLog
	lastFilter store.AuditLogFilter
}

func (r *memoryAuditRepository) CreateAuditLog(_ context.Context, value *domain.AuditLog) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	copy := *value
	copy.ID = fmt.Sprintf("audit-%d", len(r.logs)+1)
	copy.Metadata = append(json.RawMessage(nil), value.Metadata...)
	if copy.CreatedAt.IsZero() {
		copy.CreatedAt = time.Now().UTC()
	}
	r.logs = append(r.logs, copy)
	value.ID, value.CreatedAt = copy.ID, copy.CreatedAt
	return nil
}

func (r *memoryAuditRepository) ListAuditLogs(_ context.Context, filter store.AuditLogFilter) ([]domain.AuditLog, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastFilter = filter
	result := append([]domain.AuditLog(nil), r.logs...)
	return result, nil
}

func (r *memoryAuditRepository) CountAuditLogs(_ context.Context, filter store.AuditLogFilter) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lastFilter = filter
	return int64(len(r.logs)), nil
}

func (r *memoryAuditRepository) DeleteAuditLogsBefore(_ context.Context, before time.Time) (int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	kept := r.logs[:0]
	var deleted int64
	for _, log := range r.logs {
		if log.CreatedAt.Before(before) {
			deleted++
			continue
		}
		kept = append(kept, log)
	}
	r.logs = kept
	return deleted, nil
}

func (r *memoryAuditRepository) snapshot() []domain.AuditLog {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]domain.AuditLog(nil), r.logs...)
}

func auditedTestEnvironment(t *testing.T) (*testEnvironment, *memoryAuditRepository) {
	t.Helper()
	environment := newAuthenticatedEnvironment(t)
	audit := &memoryAuditRepository{}
	environment.handler = NewAdminHandler(Config{
		Auth: environment.auth, Audit: audit, Management: environment.management,
		AdminRepository: environment.repository, Accounts: environment.accounts,
		AccountProbe: environment.probe, Settings: environment.settings, ConfigChanges: environment.notifier,
		SessionCookie: "session", CSRFCookie: "csrf", CSRFHeader: "X-CSRF-Token",
	})
	return environment, audit
}

func TestAuditMiddlewareRecordsStatusWithoutSensitiveRequestData(t *testing.T) {
	environment, audit := auditedTestEnvironment(t)
	secretMarkers := []string{"PASSWORD_MARKER", "TOTP_MARKER", "SSO_MARKER", "KEY_MARKER", "BODY_MARKER"}

	created := environment.request(t, http.MethodPost, "/keys", map[string]any{"name": "Production", "secret": secretMarkers[3]}, environment.cookie, environment.csrf)
	if created.Code != http.StatusBadRequest { // Unknown secret field is rejected, but the authenticated attempt is still audited.
		t.Fatalf("key mutation status = %d %s", created.Code, created.Body.String())
	}
	missingCSRF := environment.request(t, http.MethodPatch, "/settings", map[string]any{"password": secretMarkers[0], "totp": secretMarkers[1], "sso": secretMarkers[2]}, environment.cookie, "")
	if missingCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d %s", missingCSRF.Code, missingCSRF.Body.String())
	}
	debugger := environment.request(t, http.MethodPost, "/debugger", map[string]any{"api_key": secretMarkers[3], "body": map[string]any{"value": secretMarkers[4]}}, environment.cookie, environment.csrf)
	if debugger.Code != http.StatusServiceUnavailable {
		t.Fatalf("debugger status = %d %s", debugger.Code, debugger.Body.String())
	}

	logs := audit.snapshot()
	if len(logs) != 3 {
		t.Fatalf("audit entries = %d, want 3: %+v", len(logs), logs)
	}
	statuses := map[int]bool{}
	for _, log := range logs {
		if log.AdminID == "" || log.Action == "" || log.ResourceType == "" || log.IPAddress == "" {
			t.Fatalf("incomplete audit entry: %+v", log)
		}
		var metadata map[string]any
		if err := json.Unmarshal(log.Metadata, &metadata); err != nil {
			t.Fatalf("decode metadata: %v", err)
		}
		status := int(metadata["status"].(float64))
		statuses[status] = true
		for _, marker := range secretMarkers {
			if strings.Contains(string(log.Metadata), marker) {
				t.Fatalf("audit metadata leaked %q: %s", marker, log.Metadata)
			}
		}
		for key := range metadata {
			switch key {
			case "method", "route", "status", "success", "duration_ms":
			default:
				t.Fatalf("unexpected audit metadata key %q in %s", key, log.Metadata)
			}
		}
	}
	if !statuses[http.StatusBadRequest] || !statuses[http.StatusForbidden] || !statuses[http.StatusServiceUnavailable] {
		t.Fatalf("recorded statuses = %v", statuses)
	}
}

func TestAuditListFiltersAndRetentionCleanup(t *testing.T) {
	environment, audit := auditedTestEnvironment(t)
	old := time.Now().UTC().Add(-45 * 24 * time.Hour)
	audit.logs = append(audit.logs, domain.AuditLog{ID: "old", AdminID: "admin-1", Action: "api_key.update", ResourceType: "api_key", ResourceID: "key-1", Metadata: json.RawMessage(`{"status":200,"success":true}`), CreatedAt: old})

	listed := environment.request(t, http.MethodGet, "/audit-logs?q=key&resource_type=api_key&success=true&status_code=200&limit=25&page=2", nil, environment.cookie, "")
	if listed.Code != http.StatusOK || listed.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("audit list = %d %s", listed.Code, listed.Body.String())
	}
	filter := audit.lastFilter
	if filter.Query != "key" || filter.ResourceType != "api_key" || filter.Success == nil || !*filter.Success || filter.StatusCode != 200 || filter.Limit != 25 || filter.Offset != 25 {
		t.Fatalf("audit filter = %+v", filter)
	}

	before := time.Now().UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
	cleaned := environment.request(t, http.MethodDelete, "/audit-logs?before="+before, nil, environment.cookie, environment.csrf)
	if cleaned.Code != http.StatusOK || !strings.Contains(cleaned.Body.String(), `"deleted":1`) {
		t.Fatalf("audit cleanup = %d %s", cleaned.Code, cleaned.Body.String())
	}
	logs := audit.snapshot()
	if len(logs) != 1 || logs[0].Action != "audit_log.cleanup" {
		t.Fatalf("post-cleanup audit entries = %+v", logs)
	}
	var metadata map[string]any
	_ = json.Unmarshal(logs[0].Metadata, &metadata)
	if metadata["status"] != float64(http.StatusOK) || metadata["success"] != true {
		t.Fatalf("cleanup metadata = %v", metadata)
	}
}

func TestAuditClassificationNeverPersistsPathLikeResourceID(t *testing.T) {
	action, resourceType, resourceID := classifyAuditMutation(http.MethodDelete, "/media/../../outside", "admin-1")
	if action == "" || resourceType != "media" || resourceID != "" {
		t.Fatalf("path-like target classified as %q %q %q", action, resourceType, resourceID)
	}
	action, resourceType, resourceID = classifyAuditMutation(http.MethodPost, "/keys/key-1/reveal", "admin-1")
	if action != "api_key.reveal" || resourceType != "api_key" || resourceID != "key-1" {
		t.Fatalf("key reveal target = %q %q %q", action, resourceType, resourceID)
	}
}
