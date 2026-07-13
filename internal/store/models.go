package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

const modelColumns = `
	id, upstream_model, display_name, capability, credential_kinds,
	minimum_tier, aliases, prefer_best, catalog_managed, enabled, created_at, updated_at`

type ModelFilter struct {
	Pagination
	Capability domain.Capability
	Enabled    *bool
	Query      string
}

func (p *Postgres) CreateModel(ctx context.Context, model *domain.ModelSpec) error {
	if model == nil || strings.TrimSpace(model.ID) == "" {
		return errorsNew("model ID is required")
	}
	kinds, err := marshalJSON(model.CredentialKinds, "[]")
	if err != nil {
		return err
	}
	aliases, err := marshalJSON(model.Aliases, "[]")
	if err != nil {
		return err
	}
	model.CatalogManaged = false
	err = p.db.QueryRow(ctx, `
		INSERT INTO models (
			id, upstream_model, display_name, capability, credential_kinds,
			minimum_tier, aliases, prefer_best, enabled
		) VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7::jsonb, $8, $9)
		RETURNING created_at, updated_at`,
		strings.TrimSpace(model.ID), strings.TrimSpace(model.UpstreamModel),
		strings.TrimSpace(model.DisplayName), model.Capability, kinds,
		model.MinimumTier, aliases, model.PreferBest, model.Enabled,
	).Scan(&model.CreatedAt, &model.UpdatedAt)
	return translateError(err)
}

func (p *Postgres) GetModel(ctx context.Context, id string) (*domain.ModelSpec, error) {
	return scanModel(p.db.QueryRow(ctx, `SELECT `+modelColumns+` FROM models WHERE id = $1`, id))
}

func (p *Postgres) ListModels(ctx context.Context, filter ModelFilter) ([]domain.ModelSpec, error) {
	filter.Pagination = filter.Pagination.normalized()
	where, args := modelFilterSQL(filter)
	query := `SELECT ` + modelColumns + ` FROM models ` + where
	args = append(args, filter.Limit, filter.Offset)
	query += fmt.Sprintf(" ORDER BY display_name, id LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	rows, err := p.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer rows.Close()
	models := make([]domain.ModelSpec, 0)
	for rows.Next() {
		model, err := scanModel(rows)
		if err != nil {
			return nil, err
		}
		models = append(models, *model)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate models: %w", err)
	}
	return models, nil
}

func (p *Postgres) CountModels(ctx context.Context, filter ModelFilter) (int64, error) {
	where, args := modelFilterSQL(filter)
	var total int64
	if err := p.db.QueryRow(ctx, `SELECT COUNT(*) FROM models `+where, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("count models: %w", err)
	}
	return total, nil
}

func modelFilterSQL(filter ModelFilter) (string, []any) {
	query := "WHERE TRUE"
	args := make([]any, 0, 3)
	if filter.Capability != "" {
		args = append(args, filter.Capability)
		query += fmt.Sprintf(" AND capability = $%d", len(args))
	}
	if filter.Enabled != nil {
		args = append(args, *filter.Enabled)
		query += fmt.Sprintf(" AND enabled = $%d", len(args))
	}
	if search := strings.TrimSpace(filter.Query); search != "" {
		args = append(args, "%"+search+"%")
		index := len(args)
		query += fmt.Sprintf(" AND (id ILIKE $%d OR display_name ILIKE $%d OR upstream_model ILIKE $%d OR aliases::text ILIKE $%d)", index, index, index, index)
	}
	return query, args
}

func (p *Postgres) UpdateModel(ctx context.Context, model *domain.ModelSpec) error {
	if model == nil || strings.TrimSpace(model.ID) == "" {
		return errorsNew("model ID is required")
	}
	kinds, err := marshalJSON(model.CredentialKinds, "[]")
	if err != nil {
		return err
	}
	aliases, err := marshalJSON(model.Aliases, "[]")
	if err != nil {
		return err
	}
	model.CatalogManaged = false
	err = p.db.QueryRow(ctx, `
		UPDATE models SET upstream_model = $2, display_name = $3,
			capability = $4, credential_kinds = $5::jsonb, minimum_tier = $6,
			aliases = $7::jsonb, prefer_best = $8, catalog_managed = FALSE,
			enabled = $9, updated_at = now()
		WHERE id = $1 RETURNING updated_at`,
		model.ID, model.UpstreamModel, model.DisplayName, model.Capability,
		kinds, model.MinimumTier, aliases, model.PreferBest, model.Enabled,
	).Scan(&model.UpdatedAt)
	return translateError(err)
}

func (p *Postgres) DeleteModel(ctx context.Context, id string) error {
	tag, err := p.db.Exec(ctx, `DELETE FROM models WHERE id = $1`, id)
	if err != nil {
		return translateError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func scanModel(row rowScanner) (*domain.ModelSpec, error) {
	var model domain.ModelSpec
	var capability string
	var kinds, aliases []byte
	err := row.Scan(&model.ID, &model.UpstreamModel, &model.DisplayName, &capability,
		&kinds, &model.MinimumTier, &aliases, &model.PreferBest, &model.CatalogManaged,
		&model.Enabled, &model.CreatedAt, &model.UpdatedAt)
	if err != nil {
		return nil, translateError(err)
	}
	model.Capability = domain.Capability(capability)
	if err := json.Unmarshal(kinds, &model.CredentialKinds); err != nil {
		return nil, fmt.Errorf("decode model credential kinds: %w", err)
	}
	if err := json.Unmarshal(aliases, &model.Aliases); err != nil {
		return nil, fmt.Errorf("decode model aliases: %w", err)
	}
	return &model, nil
}
