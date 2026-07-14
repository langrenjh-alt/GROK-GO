package api

import (
	"net/http"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/store"
)

func (h *Handler) dashboard(w http.ResponseWriter, r *http.Request) {
	if !h.requireManagement(w) {
		return
	}
	now := time.Now().UTC()
	from := now.Add(-24 * time.Hour)
	stats, err := h.config.Management.GetRequestLogStats(r.Context(), from, now)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	logs, err := h.config.Management.ListRequestLogs(r.Context(), store.RequestLogFilter{Pagination: store.Pagination{Limit: 10}, CreatedFrom: &from, CreatedTo: &now})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	totalAccounts, err := h.config.Management.CountAccounts(r.Context(), store.AccountFilter{})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	activeAccounts, err := h.config.Management.CountAccounts(r.Context(), store.AccountFilter{Status: domain.AccountActive})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	activeKeys, err := h.config.Management.CountActiveClientKeys(r.Context(), now)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	hourlyRequests := make([]int64, 24)
	hourlyCacheEligibleRequests := make([]int64, 24)
	hourlyInputTokens := make([]int64, 24)
	hourlyCachedTokens := make([]int64, 24)
	hourlyUsageSamples := make([]int64, 24)
	hourlyCacheSamples := make([]int64, 24)
	hourlyCacheRequestHits := make([]int64, 24)
	hourlyCacheWarmupCandidates := make([]int64, 24)
	hourlyCacheAffinityReuses := make([]int64, 24)
	hourlyCacheAffinityMisses := make([]int64, 24)
	hourlyCacheHitRate := make([]float64, 24)
	hourlyCacheRequestHitRate := make([]float64, 24)
	hourlyCacheUsageCoverage := make([]float64, 24)
	for _, item := range stats.Hourly {
		if item.HoursAgo < 0 || item.HoursAgo >= 24 {
			continue
		}
		index := 23 - item.HoursAgo
		hourlyRequests[index] = item.Requests
		hourlyCacheEligibleRequests[index] = item.CacheEligibleRequests
		hourlyInputTokens[index] = item.InputTokens
		hourlyCachedTokens[index] = item.CachedTokens
		hourlyUsageSamples[index] = item.UsageSamples
		hourlyCacheSamples[index] = item.CacheSamples
		hourlyCacheRequestHits[index] = item.CacheRequestHits
		hourlyCacheWarmupCandidates[index] = item.CacheWarmupCandidates
		hourlyCacheAffinityReuses[index] = item.CacheAffinityReuses
		hourlyCacheAffinityMisses[index] = item.CacheAffinityMisses
		hourlyCacheHitRate[index] = store.CacheHitRate(item.CachedTokens, item.InputTokens)
		hourlyCacheRequestHitRate[index] = store.CacheRequestHitRate(item.CacheRequestHits, item.CacheSamples)
		hourlyCacheUsageCoverage[index] = store.CacheUsageCoverage(item.UsageSamples, item.CacheEligibleRequests)
	}
	successRate, averageLatency := 0.0, int64(0)
	if stats.Requests > 0 {
		successRate = float64(stats.Successes) * 100 / float64(stats.Requests)
		averageLatency = stats.DurationMS / stats.Requests
	}
	gatewayHealthy := h.config.Gateway != nil
	if h.config.Redis != nil && h.config.Redis.Health(r.Context()) != nil {
		gatewayHealthy = false
	}
	cacheTokenReuseRate := store.CacheHitRate(stats.CachedTokens, stats.CacheInputTokens)
	writeData(w, http.StatusOK, map[string]any{
		"requests_24h":                   stats.Requests,
		"success_rate":                   successRate,
		"avg_latency_ms":                 averageLatency,
		"tokens_24h":                     stats.InputTokens + stats.OutputTokens,
		"input_tokens_24h":               stats.CacheInputTokens,
		"cached_tokens_24h":              stats.CachedTokens,
		"usage_samples_24h":              stats.UsageSamples,
		"cache_samples_24h":              stats.CacheSamples,
		"cache_request_hits_24h":         stats.CacheRequestHits,
		"cache_warmup_candidates_24h":    stats.CacheWarmupCandidates,
		"cache_affinity_reuses_24h":      stats.CacheAffinityReuses,
		"cache_affinity_misses_24h":      stats.CacheAffinityMisses,
		"cache_eligible_requests_24h":    stats.CacheEligibleRequests,
		"cache_hit_rate":                 cacheTokenReuseRate,
		"cache_token_reuse_rate":         cacheTokenReuseRate,
		"cache_request_hit_rate":         store.CacheRequestHitRate(stats.CacheRequestHits, stats.CacheSamples),
		"cache_usage_coverage":           store.CacheUsageCoverage(stats.UsageSamples, stats.CacheEligibleRequests),
		"cache_affinity_miss_rate":       store.CacheAffinityMissRate(stats.CacheAffinityMisses, stats.CacheAffinityReuses),
		"active_accounts":                activeAccounts,
		"total_accounts":                 totalAccounts,
		"active_keys":                    activeKeys,
		"gateway_healthy":                gatewayHealthy,
		"hourly_requests":                hourlyRequests,
		"hourly_cache_eligible_requests": hourlyCacheEligibleRequests,
		"hourly_input_tokens":            hourlyInputTokens,
		"hourly_cached_tokens":           hourlyCachedTokens,
		"hourly_usage_samples":           hourlyUsageSamples,
		"hourly_cache_samples":           hourlyCacheSamples,
		"hourly_cache_request_hits":      hourlyCacheRequestHits,
		"hourly_cache_warmup_candidates": hourlyCacheWarmupCandidates,
		"hourly_cache_affinity_reuses":   hourlyCacheAffinityReuses,
		"hourly_cache_affinity_misses":   hourlyCacheAffinityMisses,
		"hourly_cache_hit_rate":          hourlyCacheHitRate,
		"hourly_cache_token_reuse_rate":  hourlyCacheHitRate,
		"hourly_cache_request_hit_rate":  hourlyCacheRequestHitRate,
		"hourly_cache_usage_coverage":    hourlyCacheUsageCoverage,
		"recent_logs":                    logs,
	})
}
