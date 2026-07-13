package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

const auditLogColumns = `
	id, admin_id, action, resource_type, resource_id, ip_address, metadata, created_at`

type AuditLogFilter struct {
	Pagination
	AdminID      string
	Action       string
	ResourceType string
	ResourceID   string
	Query        string
	Success      *bool
	StatusCode   int
	CreatedFrom  *time.Time
	CreatedTo    *time.Time
}

func (p *Postgres) CreateAuditLog(ctx context.Context, log *domain.AuditLog) error {
	if log == nil || strings.TrimSpace(log.Action) == "" || strings.TrimSpace(log.ResourceType) == "" {
		return errorsNew("audit action and resource type are required")
	}
	id, err := newID(log.ID)
	if err != nil {
		return err
	}
	metadata := log.Metadata
	if len(metadata) == 0 {
		metadata = []byte("{}")
	}
	createdAt := ensureTime(log.CreatedAt)
	_, err = p.db.Exec(ctx, `
		INSERT INTO audit_logs (
			id, admin_id, action, resource_type, resource_id, ip_address, metadata, created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8)`,
		id, nullableString(log.AdminID), strings.TrimSpace(log.Action),
		strings.TrimSpace(log.ResourceType), strings.TrimSpace(log.ResourceID),
		strings.TrimSpace(log.IPAddress), string(metadata), createdAt,
	)
	if err != nil {
		return translateError(err)
	}
	log.ID, log.CreatedAt = id, createdAt
	return nil
}

func (p *Postgres) ListAuditLogs(ctx context.Context, filter AuditLogFilter) ([]domain.AuditLog, error) {
	filter.Pagination = filter.Pagination.normalized()
	where, args := auditLogFilterSQL(filter)
	query := `SELECT ` + auditLogColumns + ` FROM audit_logs ` + where
	args = append(args, filter.Limit, filter.Offset)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	rows, err := p.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list audit logs: %w", err)
	}
	defer rows.Close()
	logs := make([]domain.AuditLog, 0)
	for rows.Next() {
		log, err := scanAuditLog(rows)
		if err != nil {
			return nil, err
		}
		logs = append(logs, *log)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate audit logs: %w", err)
	}
	return logs, nil
}

func (p *Postgres) CountAuditLogs(ctx context.Context, filter AuditLogFilter) (int64, error) {
	where, args := auditLogFilterSQL(filter)
	var total int64
	if err := p.db.QueryRow(ctx, `SELECT COUNT(*) FROM audit_logs `+where, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("count audit logs: %w", err)
	}
	return total, nil
}

func auditLogFilterSQL(filter AuditLogFilter) (string, []any) {
	query := "WHERE TRUE"
	args := make([]any, 0, 10)
	add := func(condition string, value any) {
		args = append(args, value)
		query += " AND " + fmt.Sprintf(condition, len(args))
	}
	if value := strings.TrimSpace(filter.AdminID); value != "" {
		add("admin_id = $%d", value)
	}
	if value := strings.TrimSpace(filter.Action); value != "" {
		add("action = $%d", value)
	}
	if value := strings.TrimSpace(filter.ResourceType); value != "" {
		add("resource_type = $%d", value)
	}
	if value := strings.TrimSpace(filter.ResourceID); value != "" {
		add("resource_id = $%d", value)
	}
	if value := strings.TrimSpace(filter.Query); value != "" {
		args = append(args, "%"+value+"%")
		index := len(args)
		query += fmt.Sprintf(" AND (action ILIKE $%d OR resource_type ILIKE $%d OR resource_id ILIKE $%d OR COALESCE(admin_id, '') ILIKE $%d OR ip_address ILIKE $%d)", index, index, index, index, index)
	}
	if filter.Success != nil {
		add("metadata->>'success' = $%d", strconv.FormatBool(*filter.Success))
	}
	if filter.StatusCode > 0 {
		add("metadata->>'status' = $%d", strconv.Itoa(filter.StatusCode))
	}
	if filter.CreatedFrom != nil {
		add("created_at >= $%d", filter.CreatedFrom.UTC())
	}
	if filter.CreatedTo != nil {
		add("created_at < $%d", filter.CreatedTo.UTC())
	}
	return query, args
}

func (p *Postgres) DeleteAuditLogsBefore(ctx context.Context, before time.Time) (int64, error) {
	tag, err := p.db.Exec(ctx, `DELETE FROM audit_logs WHERE created_at < $1`, before.UTC())
	if err != nil {
		return 0, translateError(err)
	}
	return tag.RowsAffected(), nil
}

func scanAuditLog(row rowScanner) (*domain.AuditLog, error) {
	var log domain.AuditLog
	var adminID *string
	if err := row.Scan(&log.ID, &adminID, &log.Action, &log.ResourceType, &log.ResourceID, &log.IPAddress, &log.Metadata, &log.CreatedAt); err != nil {
		return nil, translateError(err)
	}
	if adminID != nil {
		log.AdminID = *adminID
	}
	return &log, nil
}
