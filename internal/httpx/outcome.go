package httpx

import (
	"context"
	"sync"
)

type outcomeContextKey struct{}

type Outcome struct {
	StatusCode int
	ErrorCode  string
}

type OutcomeTracker struct {
	mu      sync.RWMutex
	outcome Outcome
}

func WithOutcome(ctx context.Context) (context.Context, *OutcomeTracker) {
	tracker := &OutcomeTracker{}
	return context.WithValue(ctx, outcomeContextKey{}, tracker), tracker
}

func ReportOutcome(ctx context.Context, statusCode int, errorCode string) {
	tracker, _ := ctx.Value(outcomeContextKey{}).(*OutcomeTracker)
	if tracker == nil {
		return
	}
	tracker.mu.Lock()
	tracker.outcome = Outcome{StatusCode: statusCode, ErrorCode: errorCode}
	tracker.mu.Unlock()
}

func (t *OutcomeTracker) Snapshot() Outcome {
	if t == nil {
		return Outcome{}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.outcome
}
