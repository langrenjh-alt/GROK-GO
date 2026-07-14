package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

const requestLogColumns = `
	id, request_id, client_key_id, account_id, model, endpoint, status_code,
	duration_ms, input_tokens, output_tokens, cached_tokens, usage_parsed, error_code,
	error_summary, metadata, created_at`

type RequestLogFilter struct {
	Pagination
	Query       string
	RequestID   string
	ClientKeyID string
	AccountID   string
	Model       string
	Endpoint    string
	StatusCode  int
	StatusMin   int
	StatusMax   int
	CreatedFrom *time.Time
	CreatedTo   *time.Time
}

type RequestLogHourStats struct {
	HoursAgo              int
	Requests              int64
	CacheEligibleRequests int64
	InputTokens           int64
	CachedTokens          int64
	UsageSamples          int64
	CacheSamples          int64
	CacheRequestHits      int64
	CacheWarmupCandidates int64
	CacheAffinityReuses   int64
	CacheAffinityMisses   int64
}

type RequestLogStats struct {
	Requests              int64
	Successes             int64
	DurationMS            int64
	InputTokens           int64
	OutputTokens          int64
	CacheEligibleRequests int64
	CacheInputTokens      int64
	CachedTokens          int64
	UsageSamples          int64
	CacheSamples          int64
	CacheRequestHits      int64
	CacheWarmupCandidates int64
	CacheAffinityReuses   int64
	CacheAffinityMisses   int64
	Hourly                []RequestLogHourStats
}

func (p *Postgres) CreateRequestLog(ctx context.Context, log *domain.RequestLog) error {
	if log == nil {
		return errorsNew("request log is required")
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
	minuteStart, dayStart, monthStart := usageBucketStarts(createdAt)
	_, err = p.db.Exec(ctx, `
		WITH inserted_log AS (
			INSERT INTO request_logs (
				id, request_id, client_key_id, account_id, model, endpoint, status_code,
				duration_ms, input_tokens, output_tokens, cached_tokens, usage_parsed,
				error_code, error_summary, metadata, created_at
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15::jsonb, $16)
			RETURNING client_key_id, input_tokens, output_tokens, cached_tokens
		), buckets (bucket_start, bucket_kind) AS (
			VALUES ($17::timestamptz, 'minute'), ($18::timestamptz, 'day'), ($19::timestamptz, 'month')
		)
		INSERT INTO usage_buckets (
			client_key_id, bucket_start, bucket_kind, requests,
			input_tokens, output_tokens, cached_tokens
		)
		SELECT inserted_log.client_key_id, buckets.bucket_start, buckets.bucket_kind, 1,
			inserted_log.input_tokens, inserted_log.output_tokens, inserted_log.cached_tokens
		FROM inserted_log CROSS JOIN buckets
		WHERE inserted_log.client_key_id IS NOT NULL
		ON CONFLICT (client_key_id, bucket_kind, bucket_start) DO UPDATE SET
			requests = usage_buckets.requests + 1,
			input_tokens = usage_buckets.input_tokens + EXCLUDED.input_tokens,
			output_tokens = usage_buckets.output_tokens + EXCLUDED.output_tokens,
			cached_tokens = usage_buckets.cached_tokens + EXCLUDED.cached_tokens,
			updated_at = now()`,
		id, log.RequestID, nullableString(log.ClientKeyID), nullableString(log.AccountID),
		log.Model, log.Endpoint, log.StatusCode, log.DurationMS, log.InputTokens,
		log.OutputTokens, log.CachedTokens, log.UsageParsed, log.ErrorCode, log.ErrorSummary, string(metadata), createdAt,
		minuteStart, dayStart, monthStart,
	)
	if err != nil {
		return translateError(err)
	}
	log.ID = id
	log.CreatedAt = createdAt
	return nil
}

func usageBucketStarts(createdAt time.Time) (time.Time, time.Time, time.Time) {
	createdAt = createdAt.UTC()
	minute := createdAt.Truncate(time.Minute)
	day := time.Date(createdAt.Year(), createdAt.Month(), createdAt.Day(), 0, 0, 0, 0, time.UTC)
	month := time.Date(createdAt.Year(), createdAt.Month(), 1, 0, 0, 0, 0, time.UTC)
	return minute, day, month
}

func (p *Postgres) GetRequestLog(ctx context.Context, id string) (*domain.RequestLog, error) {
	return scanRequestLog(p.db.QueryRow(ctx, `SELECT `+requestLogColumns+` FROM request_logs WHERE id = $1`, id))
}

func (p *Postgres) ListRequestLogs(ctx context.Context, filter RequestLogFilter) ([]domain.RequestLog, error) {
	filter.Pagination = filter.Pagination.normalized()
	where, args := requestLogFilterSQL(filter)
	query := `SELECT ` + requestLogColumns + ` FROM request_logs ` + where
	args = append(args, filter.Limit, filter.Offset)
	query += fmt.Sprintf(" ORDER BY created_at DESC LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	rows, err := p.db.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list request logs: %w", err)
	}
	defer rows.Close()
	logs := make([]domain.RequestLog, 0)
	for rows.Next() {
		log, err := scanRequestLog(rows)
		if err != nil {
			return nil, err
		}
		logs = append(logs, *log)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate request logs: %w", err)
	}
	return logs, nil
}

func (p *Postgres) CountRequestLogs(ctx context.Context, filter RequestLogFilter) (int64, error) {
	where, args := requestLogFilterSQL(filter)
	var total int64
	if err := p.db.QueryRow(ctx, `SELECT COUNT(*) FROM request_logs `+where, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("count request logs: %w", err)
	}
	return total, nil
}

func requestLogFilterSQL(filter RequestLogFilter) (string, []any) {
	query := "WHERE TRUE"
	args := make([]any, 0, 11)
	add := func(condition string, value any) {
		args = append(args, value)
		query += " AND " + fmt.Sprintf(condition, len(args))
	}
	if value := strings.TrimSpace(filter.Query); value != "" {
		args = append(args, "%"+value+"%")
		index := len(args)
		query += fmt.Sprintf(" AND (request_id ILIKE $%d OR model ILIKE $%d OR endpoint ILIKE $%d OR error_code ILIKE $%d OR error_summary ILIKE $%d)", index, index, index, index, index)
	}
	if value := strings.TrimSpace(filter.RequestID); value != "" {
		add("request_id = $%d", value)
	}
	if value := strings.TrimSpace(filter.ClientKeyID); value != "" {
		add("client_key_id = $%d", value)
	}
	if value := strings.TrimSpace(filter.AccountID); value != "" {
		add("account_id = $%d", value)
	}
	if value := strings.TrimSpace(filter.Model); value != "" {
		add("model = $%d", value)
	}
	if value := strings.TrimSpace(filter.Endpoint); value != "" {
		add("endpoint = $%d", value)
	}
	if filter.StatusCode != 0 {
		add("status_code = $%d", filter.StatusCode)
	} else {
		if filter.StatusMin > 0 {
			add("status_code >= $%d", filter.StatusMin)
		}
		if filter.StatusMax > 0 {
			add("status_code <= $%d", filter.StatusMax)
		}
	}
	if filter.CreatedFrom != nil {
		add("created_at >= $%d", *filter.CreatedFrom)
	}
	if filter.CreatedTo != nil {
		add("created_at < $%d", *filter.CreatedTo)
	}
	return query, args
}

func (p *Postgres) GetRequestLogStats(ctx context.Context, from, to time.Time) (*RequestLogStats, error) {
	from, to = from.UTC(), to.UTC()
	if !to.After(from) {
		return nil, errorsNew("request log stats require a valid time range")
	}
	stats := &RequestLogStats{}
	err := p.db.QueryRow(ctx, `
		WITH cache_rows AS (
			SELECT status_code, duration_ms, input_tokens, output_tokens, usage_parsed,
				status_code >= 200 AND status_code < 400
				AND endpoint IN ('/v1/chat/completions', '/v1/responses', '/v1/messages') AS cache_eligible,
				LEAST(GREATEST(cached_tokens, 0), GREATEST(input_tokens, 0)) AS normalized_cached_tokens,
				metadata @> '{"cache_identity_applied": true}'::jsonb AS cache_identity_applied,
				metadata @> '{"cache_affinity_reused": true}'::jsonb AS cache_affinity_reused,
				metadata @> '{"cache_affinity_established": true}'::jsonb AS cache_affinity_established
			FROM request_logs
			WHERE created_at >= $1 AND created_at < $2
		)
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE status_code >= 200 AND status_code < 400),
			COALESCE(SUM(duration_ms), 0),
			COALESCE(SUM(input_tokens), 0),
			COALESCE(SUM(output_tokens), 0),
			COUNT(*) FILTER (WHERE cache_eligible),
			COALESCE(SUM(input_tokens) FILTER (WHERE cache_eligible AND usage_parsed AND input_tokens > 0), 0),
			COALESCE(SUM(normalized_cached_tokens) FILTER (WHERE cache_eligible AND usage_parsed AND input_tokens > 0), 0),
			COUNT(*) FILTER (WHERE cache_eligible AND usage_parsed),
			COUNT(*) FILTER (WHERE cache_eligible AND usage_parsed AND input_tokens > 0),
			COUNT(*) FILTER (WHERE cache_eligible AND usage_parsed AND input_tokens > 0 AND normalized_cached_tokens > 0),
			COUNT(*) FILTER (WHERE cache_eligible AND usage_parsed AND input_tokens > 0 AND cache_affinity_established AND normalized_cached_tokens = 0),
			COUNT(*) FILTER (WHERE cache_eligible AND usage_parsed AND input_tokens > 0 AND cache_affinity_reused),
			COUNT(*) FILTER (WHERE cache_eligible AND usage_parsed AND input_tokens > 0 AND cache_affinity_reused AND normalized_cached_tokens = 0)
		FROM cache_rows`, from, to).Scan(
		&stats.Requests, &stats.Successes, &stats.DurationMS, &stats.InputTokens,
		&stats.OutputTokens, &stats.CacheEligibleRequests, &stats.CacheInputTokens,
		&stats.CachedTokens, &stats.UsageSamples, &stats.CacheSamples, &stats.CacheRequestHits,
		&stats.CacheWarmupCandidates, &stats.CacheAffinityReuses, &stats.CacheAffinityMisses,
	)
	if err != nil {
		return nil, fmt.Errorf("aggregate request logs: %w", err)
	}
	rows, err := p.db.Query(ctx, `
		WITH cache_rows AS (
			SELECT created_at, input_tokens, usage_parsed,
				status_code >= 200 AND status_code < 400
				AND endpoint IN ('/v1/chat/completions', '/v1/responses', '/v1/messages') AS cache_eligible,
				LEAST(GREATEST(cached_tokens, 0), GREATEST(input_tokens, 0)) AS normalized_cached_tokens,
				metadata @> '{"cache_identity_applied": true}'::jsonb AS cache_identity_applied,
				metadata @> '{"cache_affinity_reused": true}'::jsonb AS cache_affinity_reused,
				metadata @> '{"cache_affinity_established": true}'::jsonb AS cache_affinity_established
			FROM request_logs
			WHERE created_at >= $1 AND created_at < $2
		)
		SELECT
			FLOOR(EXTRACT(EPOCH FROM ($2::timestamptz - created_at)) / 3600)::integer AS hours_ago,
			COUNT(*),
			COUNT(*) FILTER (WHERE cache_eligible),
			COALESCE(SUM(input_tokens) FILTER (WHERE cache_eligible AND usage_parsed AND input_tokens > 0), 0),
			COALESCE(SUM(normalized_cached_tokens) FILTER (WHERE cache_eligible AND usage_parsed AND input_tokens > 0), 0),
			COUNT(*) FILTER (WHERE cache_eligible AND usage_parsed),
			COUNT(*) FILTER (WHERE cache_eligible AND usage_parsed AND input_tokens > 0),
			COUNT(*) FILTER (WHERE cache_eligible AND usage_parsed AND input_tokens > 0 AND normalized_cached_tokens > 0),
			COUNT(*) FILTER (WHERE cache_eligible AND usage_parsed AND input_tokens > 0 AND cache_affinity_established AND normalized_cached_tokens = 0),
			COUNT(*) FILTER (WHERE cache_eligible AND usage_parsed AND input_tokens > 0 AND cache_affinity_reused),
			COUNT(*) FILTER (WHERE cache_eligible AND usage_parsed AND input_tokens > 0 AND cache_affinity_reused AND normalized_cached_tokens = 0)
		FROM cache_rows
		GROUP BY 1
		ORDER BY 1`, from, to)
	if err != nil {
		return nil, fmt.Errorf("aggregate hourly request logs: %w", err)
	}
	defer rows.Close()
	stats.Hourly = make([]RequestLogHourStats, 0, 24)
	for rows.Next() {
		var hour RequestLogHourStats
		if err := rows.Scan(&hour.HoursAgo, &hour.Requests, &hour.CacheEligibleRequests,
			&hour.InputTokens, &hour.CachedTokens, &hour.UsageSamples,
			&hour.CacheSamples, &hour.CacheRequestHits, &hour.CacheWarmupCandidates,
			&hour.CacheAffinityReuses, &hour.CacheAffinityMisses); err != nil {
			return nil, fmt.Errorf("scan hourly request log stats: %w", err)
		}
		stats.Hourly = append(stats.Hourly, hour)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate hourly request log stats: %w", err)
	}
	return stats, nil
}

func CacheHitRate(cachedTokens, inputTokens int64) float64 {
	if inputTokens <= 0 {
		return 0
	}
	cachedTokens = NormalizedCachedTokens(inputTokens, cachedTokens)
	return float64(cachedTokens) * 100 / float64(inputTokens)
}

func CacheRequestHitRate(hits, samples int64) float64 {
	return percentage(hits, samples)
}

func CacheUsageCoverage(usageSamples, eligibleRequests int64) float64 {
	return percentage(usageSamples, eligibleRequests)
}

func CacheAffinityMissRate(misses, reuses int64) float64 {
	return percentage(misses, reuses)
}

func percentage(numerator, denominator int64) float64 {
	if numerator <= 0 || denominator <= 0 {
		return 0
	}
	if numerator > denominator {
		numerator = denominator
	}
	return float64(numerator) * 100 / float64(denominator)
}

func CacheEligibleRequest(endpoint string, statusCode int) bool {
	if statusCode < 200 || statusCode >= 400 {
		return false
	}
	switch endpoint {
	case "/v1/chat/completions", "/v1/responses", "/v1/messages":
		return true
	default:
		return false
	}
}

func NormalizedCachedTokens(inputTokens, cachedTokens int64) int64 {
	if inputTokens <= 0 || cachedTokens <= 0 {
		return 0
	}
	if cachedTokens > inputTokens {
		return inputTokens
	}
	return cachedTokens
}

func (p *Postgres) UpdateRequestLog(ctx context.Context, log *domain.RequestLog) error {
	if log == nil || strings.TrimSpace(log.ID) == "" {
		return errorsNew("request log ID is required")
	}
	metadata := log.Metadata
	if len(metadata) == 0 {
		metadata = []byte("{}")
	}
	tag, err := p.db.Exec(ctx, `
		UPDATE request_logs SET status_code = $2, duration_ms = $3,
			input_tokens = $4, output_tokens = $5, cached_tokens = $6,
			usage_parsed = $7, error_code = $8, error_summary = $9, metadata = $10::jsonb
		WHERE id = $1`,
		log.ID, log.StatusCode, log.DurationMS, log.InputTokens, log.OutputTokens,
		log.CachedTokens, log.UsageParsed, log.ErrorCode, log.ErrorSummary, string(metadata),
	)
	if err != nil {
		return translateError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) DeleteRequestLog(ctx context.Context, id string) error {
	tag, err := p.db.Exec(ctx, `DELETE FROM request_logs WHERE id = $1`, id)
	if err != nil {
		return translateError(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (p *Postgres) DeleteRequestLogsBefore(ctx context.Context, before time.Time) (int64, error) {
	tag, err := p.db.Exec(ctx, `DELETE FROM request_logs WHERE created_at < $1`, before.UTC())
	if err != nil {
		return 0, translateError(err)
	}
	return tag.RowsAffected(), nil
}

func scanRequestLog(row rowScanner) (*domain.RequestLog, error) {
	var log domain.RequestLog
	var clientKeyID, accountID *string
	err := row.Scan(&log.ID, &log.RequestID, &clientKeyID, &accountID, &log.Model,
		&log.Endpoint, &log.StatusCode, &log.DurationMS, &log.InputTokens,
		&log.OutputTokens, &log.CachedTokens, &log.UsageParsed, &log.ErrorCode, &log.ErrorSummary,
		&log.Metadata, &log.CreatedAt)
	if err != nil {
		return nil, translateError(err)
	}
	if clientKeyID != nil {
		log.ClientKeyID = *clientKeyID
	}
	if accountID != nil {
		log.AccountID = *accountID
	}
	return &log, nil
}
