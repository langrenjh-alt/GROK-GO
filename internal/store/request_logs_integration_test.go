package store

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/config"
	"github.com/langrenjh-alt/GROK-GO/internal/database"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

func TestRequestLogCacheStatsIntegration(t *testing.T) {
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
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	repository := &Postgres{db: tx}

	windowStart := time.Date(2200, time.January, 1, 0, 0, 0, 0, time.UTC)
	createdAt := windowStart.Add(30 * time.Minute)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	logs := []domain.RequestLog{
		{ID: "cache-partial-" + suffix, RequestID: "cache-partial-" + suffix, Endpoint: "/v1/chat/completions", StatusCode: http.StatusOK, DurationMS: 100, InputTokens: 100, OutputTokens: 20, CachedTokens: 40, UsageParsed: true, Metadata: []byte(`{"cache_identity_applied":true,"cache_affinity_reused":true}`), CreatedAt: createdAt},
		{ID: "cache-rate-limited-" + suffix, RequestID: "cache-rate-limited-" + suffix, Endpoint: "/v1/responses", StatusCode: http.StatusTooManyRequests, DurationMS: 300, InputTokens: 300, OutputTokens: 30, CachedTokens: 180, UsageParsed: true, CreatedAt: createdAt},
		{ID: "missing-usage-" + suffix, RequestID: "missing-usage-" + suffix, Endpoint: "/v1/responses", StatusCode: http.StatusOK, DurationMS: 500, CreatedAt: createdAt},
		{ID: "parsed-zero-" + suffix, RequestID: "parsed-zero-" + suffix, Endpoint: "/v1/messages", StatusCode: http.StatusOK, DurationMS: 80, UsageParsed: true, CreatedAt: createdAt},
		{ID: "cache-over-report-" + suffix, RequestID: "cache-over-report-" + suffix, Endpoint: "/v1/messages", StatusCode: http.StatusOK, DurationMS: 100, InputTokens: 100, CachedTokens: 250, UsageParsed: true, CreatedAt: createdAt},
		{ID: "non-cache-media-" + suffix, RequestID: "non-cache-media-" + suffix, Endpoint: "/v1/images/generations", StatusCode: http.StatusOK, DurationMS: 200, InputTokens: 400, CachedTokens: 300, UsageParsed: true, CreatedAt: createdAt},
		{ID: "cache-miss-" + suffix, RequestID: "cache-miss-" + suffix, Endpoint: "/v1/responses", StatusCode: http.StatusOK, DurationMS: 120, InputTokens: 200, OutputTokens: 10, UsageParsed: true, Metadata: []byte(`{"cache_identity_applied":true,"cache_affinity_reused":true}`), CreatedAt: createdAt},
		{ID: "cache-warmup-" + suffix, RequestID: "cache-warmup-" + suffix, Endpoint: "/v1/responses", StatusCode: http.StatusOK, DurationMS: 50, InputTokens: 50, OutputTokens: 5, UsageParsed: true, Metadata: []byte(`{"cache_identity_applied":true,"cache_affinity_reused":false,"cache_affinity_established":true}`), CreatedAt: createdAt},
	}
	for index := range logs {
		if err := repository.CreateRequestLog(ctx, &logs[index]); err != nil {
			t.Fatal(err)
		}
	}
	stored, err := repository.GetRequestLog(ctx, logs[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.UsageParsed || stored.CachedTokens != 40 {
		t.Fatalf("stored usage facts = %+v", stored)
	}

	stats, err := repository.GetRequestLogStats(ctx, windowStart, windowStart.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Requests != 8 || stats.Successes != 7 || stats.DurationMS != 1450 || stats.InputTokens != 1150 || stats.OutputTokens != 65 {
		t.Fatalf("request stats = %+v", stats)
	}
	if stats.CacheEligibleRequests != 6 || stats.UsageSamples != 5 || stats.CacheSamples != 4 || stats.CacheRequestHits != 2 || stats.CacheWarmupCandidates != 1 || stats.CacheAffinityReuses != 2 || stats.CacheAffinityMisses != 1 || stats.CacheInputTokens != 450 || stats.CachedTokens != 140 {
		t.Fatalf("cache stats = %+v", stats)
	}
	if len(stats.Hourly) != 1 || stats.Hourly[0].HoursAgo != 0 || stats.Hourly[0].CacheEligibleRequests != 6 || stats.Hourly[0].UsageSamples != 5 || stats.Hourly[0].CacheSamples != 4 || stats.Hourly[0].CacheRequestHits != 2 || stats.Hourly[0].CacheWarmupCandidates != 1 || stats.Hourly[0].CacheAffinityReuses != 2 || stats.Hourly[0].CacheAffinityMisses != 1 {
		t.Fatalf("hourly cache stats = %+v", stats.Hourly)
	}
}
