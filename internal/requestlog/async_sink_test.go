package requestlog

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

func TestAsyncSinkFlushesQueuedLogsOnClose(t *testing.T) {
	recorder := &recordingSink{}
	sink := NewAsyncSink(recorder, 128, 4)
	for index := range 100 {
		entry := &domain.RequestLog{RequestID: string(rune(index + 1)), Metadata: []byte(`{"index":1}`)}
		if err := sink.CreateRequestLog(context.Background(), entry); err != nil {
			t.Fatal(err)
		}
		entry.Metadata[0] = 'x'
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := sink.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if recorder.count() != 100 {
		t.Fatalf("flushed logs = %d, want 100", recorder.count())
	}
	if err := sink.CreateRequestLog(context.Background(), &domain.RequestLog{}); err != ErrSinkClosed {
		t.Fatalf("enqueue after close error = %v", err)
	}
}

func TestAsyncSinkDoesNotBlockWhenQueueIsFull(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	recorder := &blockingSink{started: started, release: release}
	sink := NewAsyncSink(recorder, 1, 1)

	if err := sink.CreateRequestLog(context.Background(), &domain.RequestLog{RequestID: "active"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	if err := sink.CreateRequestLog(context.Background(), &domain.RequestLog{RequestID: "queued"}); err != nil {
		t.Fatal(err)
	}

	startedAt := time.Now()
	err := sink.CreateRequestLog(context.Background(), &domain.RequestLog{RequestID: "dropped"})
	if !errors.Is(err, ErrSinkFull) {
		t.Fatalf("full queue error = %v", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("full queue enqueue blocked for %s", elapsed)
	}
	if sink.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", sink.Dropped())
	}

	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := sink.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

type recordingSink struct {
	mu      sync.Mutex
	entries []domain.RequestLog
}

type blockingSink struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingSink) CreateRequestLog(context.Context, *domain.RequestLog) error {
	s.once.Do(func() { close(s.started) })
	<-s.release
	return nil
}

func (s *recordingSink) CreateRequestLog(_ context.Context, entry *domain.RequestLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = append(s.entries, *entry)
	return nil
}

func (s *recordingSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}
