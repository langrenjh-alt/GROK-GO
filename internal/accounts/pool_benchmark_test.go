package accounts

import (
	"context"
	"testing"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

func BenchmarkPoolAcquireReleaseParallel(b *testing.B) {
	account := domain.Account{ID: "benchmark", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, ConcurrencyLimit: 100_000, HealthScore: 1}
	store := NewMemoryStore([]domain.Account{account}, map[string]domain.Credentials{"benchmark": {AccessToken: "token"}})
	pool := NewPool(store, DefaultPolicy())
	model := domain.ModelSpec{ID: "grok", CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			lease, err := pool.Acquire(ctx, Selection{Model: model})
			if err != nil {
				b.Fatal(err)
			}
			if err := lease.Release(ctx, Feedback{StatusCode: 200}); err != nil {
				b.Fatal(err)
			}
		}
	})
}
