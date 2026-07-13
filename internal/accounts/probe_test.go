package accounts

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

func TestAcquireForProbeTargetsDisabledAccountAndAppliesFeedback(t *testing.T) {
	account := domain.Account{ID: "disabled", Kind: domain.CredentialGrokSSO, Status: domain.AccountDisabled, ConcurrencyLimit: 1, HealthScore: 0.2, FailureCount: 2}
	store := NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{"disabled": {SSO: "credential"}})
	pool := NewPool(store, DefaultPolicy())

	lease, err := pool.AcquireForProbe(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if lease.Account.ID != account.ID || lease.Credentials.SSO != "credential" {
		t.Fatalf("probe lease = %+v", lease)
	}
	if _, err := pool.AcquireForProbe(context.Background(), account.ID); !errors.Is(err, ErrAccountBusy) {
		t.Fatalf("second probe exceeded concurrency: %v", err)
	}
	if err := lease.Release(context.Background(), Feedback{StatusCode: http.StatusOK}); err != nil {
		t.Fatal(err)
	}
	updated := store.Accounts[account.ID]
	if updated.Status != domain.AccountActive || updated.FailureCount != 0 || updated.HealthScore <= account.HealthScore {
		t.Fatalf("successful probe did not recover account: %+v", updated)
	}
}

func TestHTTPFeedbackParsesRateLimitAndQuotaHeaders(t *testing.T) {
	now := testTime()
	header := http.Header{}
	header.Set("Retry-After", "30")
	header.Set("X-RateLimit-Limit-Requests", "100")
	header.Set("X-RateLimit-Remaining-Requests", "25")
	header.Set("X-RateLimit-Limit-Tokens", "unlimited")
	header.Set("X-RateLimit-Reset-Requests", "90s")
	feedback := HTTPFeedback(http.StatusTooManyRequests, header, errors.New("limited"), now)
	if feedback.RetryAfter.String() != "30s" || feedback.Quota == nil {
		t.Fatalf("feedback = %+v", feedback)
	}
	if feedback.Quota.RequestsLimit != 100 || feedback.Quota.RequestsRemaining != 25 || !feedback.Quota.TokensUnlimited || feedback.Quota.ResetAt == nil || !feedback.Quota.ResetAt.Equal(now.Add(90*time.Second)) {
		t.Fatalf("quota = %+v", feedback.Quota)
	}
}

func testTime() time.Time {
	return time.Date(2026, 7, 14, 8, 0, 0, 0, time.UTC)
}
