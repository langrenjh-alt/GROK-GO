package database

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/config"
)

func TestPostgresAndRedisIntegration(t *testing.T) {
	databaseURL := os.Getenv("GROK_GO_TEST_DATABASE_URL")
	redisURL := os.Getenv("GROK_GO_TEST_REDIS_URL")
	if databaseURL == "" || redisURL == "" {
		t.Skip("integration database URLs are not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	postgres, err := OpenPostgres(ctx, config.DatabaseConfig{
		URL: databaseURL, MaxConnections: 4, MinConnections: 0,
		MaxConnLifetime: time.Minute, MaxConnIdleTime: time.Minute, HealthTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer postgres.Close()
	if err := Migrate(ctx, postgres); err != nil {
		t.Fatal(err)
	}
	embeddedMigrations, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	var migrations int
	if err := postgres.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&migrations); err != nil || migrations != len(embeddedMigrations) {
		t.Fatalf("migration count = %d, want %d, err = %v", migrations, len(embeddedMigrations), err)
	}
	adminID := fmt.Sprintf("integration-%d", time.Now().UnixNano())
	if _, err := postgres.Exec(ctx, `INSERT INTO admins (id, email, password_hash) VALUES ($1, $2, 'integration-hash')`, adminID, adminID+"@example.test"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = postgres.Exec(context.Background(), `DELETE FROM admins WHERE id = $1`, adminID) }()

	redis, err := OpenRedis(ctx, config.RedisConfig{
		URL: redisURL, KeyPrefix: fmt.Sprintf("grok-go:integration:%d:", time.Now().UnixNano()),
		DialTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, HealthTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer redis.Close()
	if err := redis.Set(ctx, "consume", []byte("value"), time.Minute); err != nil {
		t.Fatal(err)
	}
	value, existed, err := redis.GetDelete(ctx, "consume")
	if err != nil || !existed || string(value) != "value" {
		t.Fatalf("GETDEL = %q, %t, %v", value, existed, err)
	}
	if _, existed, err := redis.GetDelete(ctx, "consume"); err != nil || existed {
		t.Fatalf("second GETDEL existed = %t, err = %v", existed, err)
	}
	acquired, err := redis.AcquireSlot(ctx, "lease", 1, time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire lease = %t, %v", acquired, err)
	}
	if acquired, err := redis.AcquireSlot(ctx, "lease", 1, time.Minute); err != nil || acquired {
		t.Fatalf("second lease = %t, %v", acquired, err)
	}
	if err := redis.ReleaseSlot(ctx, "lease"); err != nil {
		t.Fatal(err)
	}
	subscription, err := redis.Subscribe(ctx, "configuration-events")
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Close()
	payload := []byte(`{"source":"fixture","scope":"runtime_settings"}`)
	if err := redis.Publish(ctx, "configuration-events", payload); err != nil {
		t.Fatal(err)
	}
	receiveCtx, receiveCancel := context.WithTimeout(ctx, 5*time.Second)
	defer receiveCancel()
	received, err := subscription.Receive(receiveCtx)
	if err != nil || string(received) != string(payload) {
		t.Fatalf("Redis pub/sub received %q, err = %v", received, err)
	}
}

func TestGrokModelCatalogMigrationPreservesCustomModels(t *testing.T) {
	databaseURL := os.Getenv("GROK_GO_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("integration database URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	postgres, err := OpenPostgres(ctx, config.DatabaseConfig{
		URL: databaseURL, MaxConnections: 1, MinConnections: 0,
		MaxConnLifetime: time.Minute, MaxConnIdleTime: time.Minute, HealthTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer postgres.Close()
	tx, err := postgres.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `
		CREATE TEMP TABLE models (
			id TEXT PRIMARY KEY,
			upstream_model TEXT NOT NULL,
			display_name TEXT NOT NULL,
			capability TEXT NOT NULL,
			credential_kinds JSONB NOT NULL DEFAULT '[]'::jsonb,
			minimum_tier TEXT NOT NULL DEFAULT '',
			aliases JSONB NOT NULL DEFAULT '[]'::jsonb,
			enabled BOOLEAN NOT NULL DEFAULT TRUE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			CONSTRAINT models_capability_check CHECK (capability IN ('chat', 'responses', 'messages', 'image', 'video'))
		);
		INSERT INTO models (id, upstream_model, display_name, capability, credential_kinds, minimum_tier, aliases, enabled) VALUES
			('grok-4.20-fast', 'grok-4.20-fast', 'Grok 4.20 Fast', 'chat', '["grok_sso"]', '', '[]', TRUE),
			('grok-4.20-auto', 'custom-auto', 'Administrator Custom Auto', 'chat', '["grok_sso"]', 'super', '[]', TRUE),
			('grok-4.3-beta', 'custom-beta', 'Administrator Custom Beta', 'chat', '["console_sso"]', '', '["custom"]', FALSE);
	`); err != nil {
		t.Fatal(err)
	}
	migrations, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	var catalogSQL string
	for _, migration := range migrations {
		if migration.Version == "005_grok_model_catalog.sql" {
			catalogSQL = migration.SQL
			break
		}
	}
	for _, statement := range splitSQLStatements(catalogSQL) {
		if _, err := tx.Exec(ctx, statement); err != nil {
			t.Fatalf("apply catalog statement: %v", err)
		}
	}

	var upstreamModel, displayName string
	var preferBest, catalogManaged, enabled bool
	if err := tx.QueryRow(ctx, `SELECT upstream_model, display_name, prefer_best, catalog_managed, enabled FROM models WHERE id = 'grok-4.20-fast'`).Scan(&upstreamModel, &displayName, &preferBest, &catalogManaged, &enabled); err != nil {
		t.Fatal(err)
	}
	if upstreamModel != "fast" || !preferBest || !catalogManaged || !enabled {
		t.Fatalf("untouched legacy preset was not upgraded: upstream=%q display=%q prefer_best=%t managed=%t enabled=%t", upstreamModel, displayName, preferBest, catalogManaged, enabled)
	}
	if err := tx.QueryRow(ctx, `SELECT upstream_model, display_name, catalog_managed FROM models WHERE id = 'grok-4.20-auto'`).Scan(&upstreamModel, &displayName, &catalogManaged); err != nil {
		t.Fatal(err)
	}
	if upstreamModel != "custom-auto" || displayName != "Administrator Custom Auto" || catalogManaged {
		t.Fatalf("customized legacy preset was overwritten: upstream=%q display=%q managed=%t", upstreamModel, displayName, catalogManaged)
	}
	if err := tx.QueryRow(ctx, `SELECT upstream_model, display_name, catalog_managed, enabled FROM models WHERE id = 'grok-4.3-beta'`).Scan(&upstreamModel, &displayName, &catalogManaged, &enabled); err != nil {
		t.Fatal(err)
	}
	if upstreamModel != "custom-beta" || displayName != "Administrator Custom Beta" || catalogManaged || enabled {
		t.Fatalf("custom model collision was overwritten: upstream=%q display=%q managed=%t enabled=%t", upstreamModel, displayName, catalogManaged, enabled)
	}
}
