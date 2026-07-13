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
	hourlyInputTokens := make([]int64, 24)
	hourlyCachedTokens := make([]int64, 24)
	hourlyUsageSamples := make([]int64, 24)
	hourlyCacheHitRate := make([]float64, 24)
	for _, item := range stats.Hourly {
		if item.HoursAgo < 0 || item.HoursAgo >= 24 {
			continue
		}
		index := 23 - item.HoursAgo
		hourlyRequests[index] = item.Requests
		hourlyInputTokens[index] = item.InputTokens
		hourlyCachedTokens[index] = item.CachedTokens
		hourlyUsageSamples[index] = item.UsageSamples
		hourlyCacheHitRate[index] = store.CacheHitRate(item.CachedTokens, item.InputTokens)
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
	writeData(w, http.StatusOK, map[string]any{
		"requests_24h":          stats.Requests,
		"success_rate":          successRate,
		"avg_latency_ms":        averageLatency,
		"tokens_24h":            stats.InputTokens + stats.OutputTokens,
		"input_tokens_24h":      stats.CacheInputTokens,
		"cached_tokens_24h":     stats.CachedTokens,
		"usage_samples_24h":     stats.UsageSamples,
		"cache_hit_rate":        store.CacheHitRate(stats.CachedTokens, stats.CacheInputTokens),
		"active_accounts":       activeAccounts,
		"total_accounts":        totalAccounts,
		"active_keys":           activeKeys,
		"gateway_healthy":       gatewayHealthy,
		"hourly_requests":       hourlyRequests,
		"hourly_input_tokens":   hourlyInputTokens,
		"hourly_cached_tokens":  hourlyCachedTokens,
		"hourly_usage_samples":  hourlyUsageSamples,
		"hourly_cache_hit_rate": hourlyCacheHitRate,
		"recent_logs":           logs,
	})
}
