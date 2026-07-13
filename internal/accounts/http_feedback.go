package accounts

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

// HTTPFeedback converts upstream protocol metadata into the account pool's
// health feedback model. It is shared by administrative probes and can be used
// by other direct upstream callers that do not go through the gateway handler.
func HTTPFeedback(statusCode int, header http.Header, err error, observedAt time.Time) Feedback {
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	return Feedback{
		StatusCode: statusCode,
		RetryAfter: retryAfterHeader(header, observedAt),
		Quota:      quotaFromHTTPHeaders(header, observedAt),
		Err:        err,
	}
}

func retryAfterHeader(header http.Header, now time.Time) time.Duration {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, parseErr := strconv.ParseInt(value, 10, 64); parseErr == nil && seconds > 0 {
		return time.Duration(seconds) * time.Second
	}
	if deadline, parseErr := http.ParseTime(value); parseErr == nil && deadline.After(now) {
		return deadline.Sub(now)
	}
	return 0
}

func quotaFromHTTPHeaders(header http.Header, observedAt time.Time) *domain.QuotaSnapshot {
	quota := domain.QuotaSnapshot{ObservedAt: observedAt}
	seen := false
	seen = readHTTPQuotaMetric(header, []string{"X-RateLimit-Limit-Requests", "RateLimit-Limit"}, &quota.RequestsLimit, &quota.RequestsUnlimited) || seen
	seen = readHTTPQuotaMetric(header, []string{"X-RateLimit-Remaining-Requests", "RateLimit-Remaining"}, &quota.RequestsRemaining, nil) || seen
	seen = readHTTPQuotaMetric(header, []string{"X-RateLimit-Limit-Tokens"}, &quota.TokensLimit, &quota.TokensUnlimited) || seen
	seen = readHTTPQuotaMetric(header, []string{"X-RateLimit-Remaining-Tokens"}, &quota.TokensRemaining, nil) || seen
	for _, name := range []string{"X-RateLimit-Reset-Requests", "X-RateLimit-Reset-Tokens", "X-RateLimit-Reset", "RateLimit-Reset"} {
		value := strings.TrimSpace(header.Get(name))
		if value == "" {
			continue
		}
		seen = true
		if reset, ok := parseHTTPQuotaReset(value, observedAt); ok && (quota.ResetAt == nil || reset.After(*quota.ResetAt)) {
			quota.ResetAt = &reset
		}
	}
	if !seen {
		return nil
	}
	return &quota
}

func readHTTPQuotaMetric(header http.Header, names []string, destination *int64, unlimited *bool) bool {
	for _, name := range names {
		value := strings.TrimSpace(header.Get(name))
		if value == "" {
			continue
		}
		lower := strings.ToLower(value)
		if unlimited != nil && (lower == "unlimited" || lower == "infinite" || lower == "inf") {
			*unlimited = true
			return true
		}
		if parsed, parseErr := strconv.ParseInt(value, 10, 64); parseErr == nil {
			*destination = max(int64(0), parsed)
		}
		return true
	}
	return false
}

func parseHTTPQuotaReset(value string, now time.Time) (time.Time, bool) {
	if duration, parseErr := time.ParseDuration(value); parseErr == nil {
		return now.Add(duration), true
	}
	if number, parseErr := strconv.ParseInt(value, 10, 64); parseErr == nil {
		if number > 1_000_000_000 {
			return time.Unix(number, 0), true
		}
		return now.Add(time.Duration(number) * time.Second), true
	}
	if reset, parseErr := http.ParseTime(value); parseErr == nil {
		return reset, true
	}
	if reset, parseErr := time.Parse(time.RFC3339, value); parseErr == nil {
		return reset, true
	}
	return time.Time{}, false
}
