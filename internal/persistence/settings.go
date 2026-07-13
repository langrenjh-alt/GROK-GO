package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

type SettingsStore struct {
	Pool *pgxpool.Pool
}

func (s SettingsStore) LoadSettings(ctx context.Context) (map[string]any, error) {
	if s.Pool == nil {
		return nil, errors.New("settings database is not configured")
	}
	rows, err := s.Pool.Query(ctx, `SELECT value FROM settings ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("load settings: %w", err)
	}
	defer rows.Close()
	result := make(map[string]any)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var values map[string]any
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, fmt.Errorf("decode settings: %w", err)
		}
		for key, value := range values {
			result[key] = value
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s SettingsStore) SaveSettings(ctx context.Context, values map[string]any) error {
	if s.Pool == nil {
		return errors.New("settings database is not configured")
	}
	if values == nil {
		values = map[string]any{}
	}
	payload, err := json.Marshal(values)
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	_, err = s.Pool.Exec(ctx, `
		INSERT INTO settings (key, value, updated_at) VALUES ('service', $1::jsonb, now())
		ON CONFLICT (key) DO UPDATE SET value = settings.value || EXCLUDED.value, updated_at = now()`, payload)
	if err != nil {
		return fmt.Errorf("save settings: %w", err)
	}
	return nil
}
