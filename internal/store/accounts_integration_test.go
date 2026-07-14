package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/config"
	"github.com/langrenjh-alt/GROK-GO/internal/database"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

func TestUpdateAccountRuntimeIntegration(t *testing.T) {
	databaseURL := os.Getenv("GROK_GO_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("integration database URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := database.OpenPostgres(ctx, config.DatabaseConfig{
		URL: databaseURL, MaxConnections: 2, MinConnections: 0,
		MaxConnLifetime: time.Minute, MaxConnIdleTime: time.Minute, HealthTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := database.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	repository := NewPostgres(pool)

	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	accountID := "runtime-account-" + suffix
	fingerprint := []byte("runtime-fingerprint-" + suffix)
	initialLastUsed := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	account := &domain.Account{
		ID: accountID, Name: "administrator name", Kind: domain.CredentialCLIOAuth,
		Tier: "super", Status: domain.AccountActive, Email: "admin@example.test",
		CredentialCipher: []byte("administrator cipher"), CredentialFingerprint: fingerprint,
		Models: []string{"grok-4.5"}, Tags: []string{"administrator-tag"},
		Priority: 71, ConcurrencyLimit: 9, HealthScore: 0.9,
		Quota:      domain.QuotaSnapshot{RequestsLimit: 100, RequestsRemaining: 90, ObservedAt: initialLastUsed},
		LastUsedAt: &initialLastUsed, LastError: "",
	}
	if err := repository.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = repository.DeleteAccount(context.Background(), accountID) }()

	cooldown := time.Now().UTC().Add(20 * time.Minute).Truncate(time.Microsecond)
	runtimeLastUsed := time.Now().UTC().Truncate(time.Microsecond)
	runtime := *account
	runtime.Name = "stale runtime name"
	runtime.Tier = "basic"
	runtime.Email = "stale-runtime@example.test"
	runtime.Models = []string{"stale-runtime-model"}
	runtime.Tags = []string{"stale-runtime-tag"}
	runtime.Priority = -100
	runtime.ConcurrencyLimit = 1
	runtime.Status = domain.AccountCooldown
	runtime.HealthScore = 0.4
	runtime.FailureCount = 3
	runtime.Quota = domain.QuotaSnapshot{RequestsLimit: 100, RequestsRemaining: 12, ObservedAt: runtimeLastUsed}
	runtime.CooldownUntil = &cooldown
	runtime.LastUsedAt = &runtimeLastUsed
	runtime.LastError = "rate limited"
	if err := repository.UpdateAccountRuntime(ctx, &runtime); err != nil {
		t.Fatal(err)
	}
	stored, err := repository.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Name != account.Name || stored.Tier != account.Tier || stored.Email != account.Email ||
		!bytes.Equal(stored.CredentialCipher, account.CredentialCipher) ||
		!bytes.Equal(stored.CredentialFingerprint, account.CredentialFingerprint) ||
		!slices.Equal(stored.Models, account.Models) || !slices.Equal(stored.Tags, account.Tags) ||
		stored.Priority != account.Priority || stored.ConcurrencyLimit != account.ConcurrencyLimit {
		t.Fatalf("runtime feedback overwrote administrator scheduling fields: %+v", stored)
	}
	if stored.Status != domain.AccountCooldown || stored.HealthScore != 0.4 || stored.FailureCount != 3 ||
		stored.Quota.RequestsRemaining != 12 || stored.CooldownUntil == nil || !stored.CooldownUntil.Equal(cooldown) ||
		stored.LastUsedAt == nil || !stored.LastUsedAt.Equal(runtimeLastUsed) || stored.LastError != "rate limited" {
		t.Fatalf("runtime feedback was not persisted: %+v", stored)
	}

	earlierCooldown := cooldown.Add(-10 * time.Minute)
	earlierLastUsed := runtimeLastUsed.Add(-10 * time.Minute)
	runtime.CooldownUntil = &earlierCooldown
	runtime.LastUsedAt = &earlierLastUsed
	if err := repository.UpdateAccountRuntime(ctx, &runtime); err != nil {
		t.Fatal(err)
	}
	stored, err = repository.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CooldownUntil == nil || !stored.CooldownUntil.Equal(cooldown) || stored.LastUsedAt == nil || !stored.LastUsedAt.Equal(runtimeLastUsed) {
		t.Fatalf("older runtime feedback moved monotonic timestamps backwards: %+v", stored)
	}

	concurrentSuccess := runtime
	concurrentSuccess.Status = domain.AccountActive
	concurrentSuccess.HealthScore = 1
	concurrentSuccess.FailureCount = 0
	concurrentSuccess.CooldownUntil = nil
	concurrentSuccess.LastError = ""
	if err := repository.UpdateAccountRuntime(ctx, &concurrentSuccess); err != nil {
		t.Fatal(err)
	}
	stored, err = repository.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.AccountCooldown || stored.HealthScore != 0.4 || stored.FailureCount != 3 ||
		stored.CooldownUntil == nil || !stored.CooldownUntil.Equal(cooldown) || stored.LastError != "rate limited" {
		t.Fatalf("older success feedback erased an unexpired cooldown: %+v", stored)
	}

	if _, err := pool.Exec(ctx, `
		UPDATE accounts SET status = 'disabled', health_score = 0.8, failure_count = 8,
			quota = '{"requests_limit":100,"requests_remaining":44,"observed_at":"2026-07-14T00:00:00Z"}'::jsonb,
			cooldown_until = NULL, last_error = 'administrator disabled'
		WHERE id = $1`, accountID); err != nil {
		t.Fatal(err)
	}
	runtime.Status = domain.AccountActive
	runtime.HealthScore = 1
	runtime.FailureCount = 0
	runtime.Quota = domain.QuotaSnapshot{RequestsUnlimited: true, ObservedAt: time.Now().UTC()}
	runtime.CooldownUntil = nil
	runtime.LastError = ""
	newLastUsed := runtimeLastUsed.Add(time.Minute)
	runtime.LastUsedAt = &newLastUsed
	if err := repository.UpdateAccountRuntime(ctx, &runtime); err != nil {
		t.Fatal(err)
	}
	stored, err = repository.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != domain.AccountDisabled || stored.HealthScore != 0.8 || stored.FailureCount != 8 ||
		stored.Quota.RequestsRemaining != 44 || stored.LastError != "administrator disabled" {
		t.Fatalf("runtime feedback re-enabled or rewrote a disabled account: %+v", stored)
	}

	// OAuth providers may rotate only the access token, so the stable import
	// fingerprint can remain unchanged while the sealed credential generation
	// changes. Runtime feedback must compare the latter as well.
	newFingerprint := fingerprint
	if _, err := pool.Exec(ctx, `
		UPDATE accounts SET name = 'post-refresh administrator name', status = 'active',
			credential_cipher = $2, credential_fingerprint = $3, priority = 99,
			health_score = 0.95, failure_count = 1,
			quota = '{"requests_unlimited":true,"observed_at":"2026-07-14T01:00:00Z"}'::jsonb,
			cooldown_until = NULL, last_used_at = $4, last_error = 'fresh credentials',
			updated_at = clock_timestamp()
		WHERE id = $1`, accountID, []byte("refreshed cipher"), newFingerprint, newLastUsed); err != nil {
		t.Fatal(err)
	}
	beforeStaleFeedback, err := repository.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	runtime.Status = domain.AccountExpired
	runtime.HealthScore = 0
	runtime.FailureCount = 100
	runtime.Quota = domain.QuotaSnapshot{RequestsRemaining: 0, ObservedAt: time.Now().UTC()}
	runtime.CooldownUntil = &cooldown
	staleLastUsed := newLastUsed.Add(time.Hour)
	runtime.LastUsedAt = &staleLastUsed
	runtime.LastError = "stale request failure"
	if err := repository.UpdateAccountRuntime(ctx, &runtime); err != nil {
		t.Fatal(err)
	}
	afterStaleFeedback, err := repository.GetAccount(ctx, accountID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeStaleFeedback, afterStaleFeedback) {
		t.Fatalf("feedback from pre-refresh credentials changed the account:\nbefore=%+v\nafter=%+v", beforeStaleFeedback, afterStaleFeedback)
	}

	missing := runtime
	missing.ID = "missing-" + suffix
	if err := repository.UpdateAccountRuntime(ctx, &missing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing runtime account error = %v", err)
	}
}
