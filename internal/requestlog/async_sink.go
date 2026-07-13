package requestlog

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
)

var (
	ErrSinkClosed = errors.New("request log sink is closed")
	ErrSinkFull   = errors.New("request log sink queue is full")
)

type AsyncSink struct {
	sink  Sink
	queue chan *domain.RequestLog

	mu        sync.RWMutex
	closed    bool
	closeOnce sync.Once
	workers   sync.WaitGroup
	done      chan struct{}
	dropped   atomic.Uint64
}

func NewAsyncSink(sink Sink, capacity, workers int) *AsyncSink {
	if capacity <= 0 {
		capacity = 4096
	}
	if workers <= 0 {
		workers = 4
	}
	result := &AsyncSink{sink: sink, queue: make(chan *domain.RequestLog, capacity), done: make(chan struct{})}
	result.workers.Add(workers)
	for range workers {
		go result.run()
	}
	go func() {
		result.workers.Wait()
		close(result.done)
	}()
	return result
}

func (s *AsyncSink) CreateRequestLog(ctx context.Context, entry *domain.RequestLog) error {
	if s == nil || s.sink == nil {
		return errors.New("request log sink is not configured")
	}
	if entry == nil {
		return errors.New("request log is required")
	}
	copy := *entry
	copy.Metadata = append([]byte(nil), entry.Metadata...)

	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return ErrSinkClosed
	}
	select {
	case s.queue <- &copy:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		s.dropped.Add(1)
		return ErrSinkFull
	}
}

func (s *AsyncSink) Dropped() uint64 {
	if s == nil {
		return 0
	}
	return s.dropped.Load()
}

func (s *AsyncSink) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		close(s.queue)
		s.mu.Unlock()
	})
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *AsyncSink) run() {
	defer s.workers.Done()
	for entry := range s.queue {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = s.sink.CreateRequestLog(ctx, entry)
		cancel()
	}
}

var _ Sink = (*AsyncSink)(nil)
