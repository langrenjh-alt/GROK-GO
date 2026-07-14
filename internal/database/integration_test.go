package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

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

func TestAccountCredentialFingerprintMigrationIntegration(t *testing.T) {
	databaseURL := os.Getenv("GROK_GO_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("integration database URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	postgres, err := OpenPostgres(ctx, config.DatabaseConfig{
		URL: databaseURL, MaxConnections: 2, MinConnections: 0,
		MaxConnLifetime: time.Minute, MaxConnIdleTime: time.Minute, HealthTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer postgres.Close()
	if err := Migrate(ctx, postgres); err != nil {
		t.Fatal(err)
	}

	var indexDefinition string
	if err := postgres.QueryRow(ctx, `
		SELECT indexdef
		FROM pg_indexes
		WHERE schemaname = current_schema()
		  AND indexname = 'accounts_credential_fingerprint_unique'`,
	).Scan(&indexDefinition); err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{"CREATE UNIQUE INDEX", "(kind, credential_fingerprint)", "WHERE (credential_fingerprint IS NOT NULL)"} {
		if !containsNormalizedSQL(indexDefinition, fragment) {
			t.Fatalf("credential fingerprint index %q does not contain %q", indexDefinition, fragment)
		}
	}

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	firstID := "fingerprint-first-" + suffix
	duplicateID := "fingerprint-duplicate-" + suffix
	otherKindID := "fingerprint-other-kind-" + suffix
	defer func() {
		_, _ = postgres.Exec(context.Background(), `DELETE FROM accounts WHERE id IN ($1, $2, $3)`, firstID, duplicateID, otherKindID)
	}()
	fingerprint := []byte("integration-fingerprint-" + suffix)
	insert := `INSERT INTO accounts (id, name, kind, credential_cipher, credential_fingerprint) VALUES ($1, $2, $3, $4, $5)`
	if _, err := postgres.Exec(ctx, insert, firstID, firstID, "cli_oauth", []byte("cipher"), fingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := postgres.Exec(ctx, insert, duplicateID, duplicateID, "cli_oauth", []byte("cipher"), fingerprint); err == nil {
		t.Fatal("duplicate credential fingerprint was accepted for the same credential kind")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23505" || pgErr.ConstraintName != "accounts_credential_fingerprint_unique" {
			t.Fatalf("duplicate credential fingerprint error = %v", err)
		}
	}
	if _, err := postgres.Exec(ctx, insert, otherKindID, otherKindID, "console_sso", []byte("cipher"), fingerprint); err != nil {
		t.Fatalf("same digest for a different credential kind should remain independent: %v", err)
	}
}

func TestAccountErrorRedactionMigrationIntegration(t *testing.T) {
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
	if err := Migrate(ctx, postgres); err != nil {
		t.Fatal(err)
	}
	tx, err := postgres.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	id := fmt.Sprintf("redact-error-%d", time.Now().UnixNano())
	if _, err := tx.Exec(ctx, `INSERT INTO accounts (id, name, kind, credential_cipher, last_error) VALUES ($1, $1, 'grok_sso', 'cipher', $2)`, id, `Post "https://grok.com/private" with Bearer secret`); err != nil {
		t.Fatal(err)
	}
	migrations, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		if migration.Version != "009_redact_account_errors.sql" {
			continue
		}
		for _, statement := range splitSQLStatements(migration.SQL) {
			if _, err := tx.Exec(ctx, statement); err != nil {
				t.Fatal(err)
			}
		}
	}
	var lastError string
	if err := tx.QueryRow(ctx, `SELECT last_error FROM accounts WHERE id = $1`, id).Scan(&lastError); err != nil {
		t.Fatal(err)
	}
	if lastError != "redacted upstream error" {
		t.Fatalf("migrated account error = %q", lastError)
	}
}

func TestRedisCoordinationPrimitivesIntegration(t *testing.T) {
	redisURL := os.Getenv("GROK_GO_TEST_REDIS_URL")
	if redisURL == "" {
		t.Skip("integration Redis URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	redis, err := OpenRedis(ctx, config.RedisConfig{
		URL: redisURL, KeyPrefix: fmt.Sprintf("grok-go:coordination:%d:", time.Now().UnixNano()),
		DialTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second, HealthTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer redis.Close()

	if err := redis.Set(ctx, "affinity", []byte("account-a"), time.Second); err != nil {
		t.Fatal(err)
	}
	if refreshed, err := redis.CompareExpire(ctx, "affinity", []byte("account-b"), 3*time.Second); err != nil || refreshed {
		t.Fatalf("compare-expire changed a value owned by another account: refreshed=%t err=%v", refreshed, err)
	}
	if refreshed, err := redis.CompareExpire(ctx, "affinity", []byte("account-a"), 3*time.Second); err != nil || !refreshed {
		t.Fatalf("compare-expire did not refresh the matching value: refreshed=%t err=%v", refreshed, err)
	}
	if ttl, err := redis.client.PTTL(ctx, redis.Key("affinity")).Result(); err != nil || ttl < 2*time.Second {
		t.Fatalf("refreshed affinity TTL = %s, err=%v", ttl, err)
	}
	longCooldown := time.Now().UTC().Add(10 * time.Second)
	shortCooldown := time.Now().UTC().Add(2 * time.Second)
	if updated, err := redis.SetIfGreater(ctx, "cooldown", longCooldown.UnixNano(), longCooldown); err != nil || !updated {
		t.Fatalf("set long cooldown: updated=%t err=%v", updated, err)
	}
	if updated, err := redis.SetIfGreater(ctx, "cooldown", shortCooldown.UnixNano(), shortCooldown); err != nil || updated {
		t.Fatalf("short cooldown replaced a longer deadline: updated=%t err=%v", updated, err)
	}
	if value, err := redis.Get(ctx, "cooldown"); err != nil || string(value) != fmt.Sprint(longCooldown.UnixNano()) {
		t.Fatalf("monotonic cooldown value = %q, err=%v", value, err)
	}

	if acquired, err := redis.AcquireLeaseSlot(ctx, "mixed-ttl", "long", 2, 10*time.Second); err != nil || !acquired {
		t.Fatalf("acquire long lease = %t, %v", acquired, err)
	}
	if acquired, err := redis.AcquireLeaseSlot(ctx, "mixed-ttl", "short", 2, 150*time.Millisecond); err != nil || !acquired {
		t.Fatalf("acquire short lease = %t, %v", acquired, err)
	}
	if acquired, err := redis.AcquireLeaseSlot(ctx, "mixed-ttl", "long", 2, 10*time.Second); err != nil || !acquired {
		t.Fatalf("same owner acquisition was not idempotent at capacity: acquired=%t err=%v", acquired, err)
	}
	time.Sleep(300 * time.Millisecond)
	if acquired, err := redis.AcquireLeaseSlot(ctx, "mixed-ttl", "probe", 1, time.Second); err != nil || acquired {
		t.Fatalf("short lease truncated the still-active long lease: acquired=%t err=%v", acquired, err)
	}
	if err := redis.ReleaseLeaseSlot(ctx, "mixed-ttl", "not-the-owner"); err != nil {
		t.Fatal(err)
	}
	if acquired, err := redis.AcquireLeaseSlot(ctx, "mixed-ttl", "probe", 1, time.Second); err != nil || acquired {
		t.Fatalf("stale release removed another owner's lease: acquired=%t err=%v", acquired, err)
	}
	if err := redis.ReleaseLeaseSlot(ctx, "mixed-ttl", "long"); err != nil {
		t.Fatal(err)
	}
	if acquired, err := redis.AcquireLeaseSlot(ctx, "mixed-ttl", "probe", 1, time.Second); err != nil || !acquired {
		t.Fatalf("released capacity was not reusable: acquired=%t err=%v", acquired, err)
	}

	const concurrencyLimit = int32(4)
	var acquiredCount atomic.Int32
	start := make(chan struct{})
	errorsFound := make(chan error, 24)
	var group sync.WaitGroup
	for candidate := 0; candidate < cap(errorsFound); candidate++ {
		group.Add(1)
		go func(owner int) {
			defer group.Done()
			<-start
			acquired, err := redis.AcquireLeaseSlot(ctx, "concurrent", fmt.Sprintf("owner-%d", owner), int(concurrencyLimit), 10*time.Second)
			if err != nil {
				errorsFound <- err
				return
			}
			if acquired {
				acquiredCount.Add(1)
			}
		}(candidate)
	}
	close(start)
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	if got := acquiredCount.Load(); got != concurrencyLimit {
		t.Fatalf("atomic lease acquisition count = %d, want %d", got, concurrencyLimit)
	}
}

func containsNormalizedSQL(value, fragment string) bool {
	normalize := func(input string) string {
		return strings.Join(strings.Fields(input), " ")
	}
	return strings.Contains(normalize(value), normalize(fragment))
}
