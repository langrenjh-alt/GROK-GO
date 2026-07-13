package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/accounts"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/upstream"
)

func newVideoPollingGateway(t *testing.T, background context.Context, client upstream.Client, localizer MediaLocalizer) (*Handler, *accounts.Pool, *MemoryVideoStore, domain.ModelSpec) {
	t.Helper()
	model := domain.ModelSpec{ID: "grok-video", UpstreamModel: "grok-video", Capability: domain.CapabilityVideo, CredentialKinds: []domain.CredentialKind{domain.CredentialCLIOAuth}, Enabled: true}
	store := accounts.NewMemoryStore(
		[]domain.Account{{ID: "video-account", Kind: domain.CredentialCLIOAuth, Status: domain.AccountActive, ConcurrencyLimit: 1}},
		map[string]domain.Credentials{"video-account": {AccessToken: "access"}},
	)
	pool := accounts.NewPool(store, accounts.DefaultPolicy())
	videos := NewMemoryVideoStore()
	handler, err := NewHandler(Config{
		Models:            NewStaticModelSource([]domain.ModelSpec{model}),
		Accounts:          pool,
		Upstream:          client,
		Videos:            videos,
		Media:             localizer,
		BackgroundContext: background,
		VideoPollInterval: 2 * time.Millisecond,
		VideoPollTimeout:  time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler, pool, videos, model
}

func TestQueuedVideoIsPolledUntilCompleted(t *testing.T) {
	background, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var mu sync.Mutex
	statusCalls := 0
	var requestProblems []string
	client := upstream.ClientFunc(func(_ context.Context, request upstream.Request) (*upstream.Response, error) {
		switch request.Operation {
		case upstream.OperationVideo:
			return &upstream.Response{StatusCode: http.StatusOK, Body: json.RawMessage(`{"request_id":"video_123","status":"queued","status_url":"https://upstream.test/status/video_123"}`)}, nil
		case upstream.OperationVideoStatus:
			mu.Lock()
			defer mu.Unlock()
			statusCalls++
			if request.VideoID != "video_123" || request.StatusURL != "" || request.Credentials.AccessToken != "access" {
				requestProblems = append(requestProblems, "status query did not preserve the bound account and video ID")
			}
			if statusCalls == 1 {
				return &upstream.Response{StatusCode: http.StatusOK, Body: json.RawMessage(`{"id":"video_123","status":"in_progress","status_url":"https://upstream.test/status/video_123"}`)}, nil
			}
			return &upstream.Response{StatusCode: http.StatusOK, Body: json.RawMessage(`{"id":"video_123","status":"completed","url":"https://cdn.test/video_123.mp4"}`)}, nil
		default:
			return &upstream.Response{StatusCode: http.StatusBadRequest}, nil
		}
	})
	var localized []string
	localizer := localizerFunc(func(_ context.Context, kind, rawURL string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		localized = append(localized, kind+":"+rawURL)
		return "https://gateway.test/media/video-local?sig=x", nil
	})
	handler, _, videos, _ := newVideoPollingGateway(t, background, client, localizer)

	created := httptest.NewRecorder()
	handler.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/v1/videos/generations", strings.NewReader(`{"model":"grok-video","prompt":"waves"}`)))
	if created.Code != http.StatusOK || !strings.Contains(created.Body.String(), "video_123") {
		t.Fatalf("create response = %d %s", created.Code, created.Body.String())
	}

	job := waitForVideoStatus(t, videos, "video_123", "completed")
	if job.AccountID != "video-account" || !strings.Contains(string(job.Payload), "gateway.test/media/video-local") {
		t.Fatalf("completed job = %+v, payload = %s", job, job.Payload)
	}
	mu.Lock()
	defer mu.Unlock()
	if statusCalls != 2 || len(requestProblems) != 0 {
		t.Fatalf("status calls = %d, problems = %v", statusCalls, requestProblems)
	}
	if len(localized) != 1 || localized[0] != "video:https://cdn.test/video_123.mp4" {
		t.Fatalf("localized URLs = %v", localized)
	}
}

func TestVideoPollingStopsWhenBackgroundContextIsCancelled(t *testing.T) {
	background, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	stopped := make(chan struct{})
	var once sync.Once
	client := upstream.ClientFunc(func(ctx context.Context, request upstream.Request) (*upstream.Response, error) {
		if request.Operation == upstream.OperationVideo {
			return &upstream.Response{StatusCode: http.StatusOK, Body: json.RawMessage(`{"id":"video_cancel","status":"queued"}`)}, nil
		}
		once.Do(func() { close(started) })
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	})
	handler, pool, _, model := newVideoPollingGateway(t, background, client, nil)
	created := httptest.NewRecorder()
	handler.ServeHTTP(created, httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"model":"grok-video","prompt":"waves"}`)))
	if created.Code != http.StatusOK {
		t.Fatalf("create response = %d %s", created.Code, created.Body.String())
	}
	waitChannel(t, started, "status polling did not start")
	cancel()
	waitChannel(t, stopped, "status polling request was not cancelled")

	deadline := time.Now().Add(time.Second)
	for {
		lease, err := pool.Acquire(context.Background(), accounts.Selection{Model: model})
		if err == nil {
			_ = lease.Release(context.Background(), accounts.Feedback{StatusCode: http.StatusOK})
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("video account lease was not released: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestFailedVideoPropagatesPermanentCredentialFeedback(t *testing.T) {
	payload := json.RawMessage(`{"id":"video_failed","status":"failed","error":{"message":"expired credential","upstream_status":401}}`)
	feedback := videoFailureFeedback(payload)
	if feedback.StatusCode != http.StatusUnauthorized || feedback.Err == nil || feedback.Err.Error() != "expired credential" {
		t.Fatalf("feedback = %+v", feedback)
	}
}

func waitForVideoStatus(t *testing.T, store *MemoryVideoStore, id, status string) VideoJob {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		job, err := store.GetVideo(context.Background(), id)
		if err == nil && job.Status == status {
			return job
		}
		if time.Now().After(deadline) {
			t.Fatalf("video %s did not reach %s; last job = %+v, err = %v", id, status, job, err)
		}
		time.Sleep(time.Millisecond)
	}
}

func waitChannel(t *testing.T, channel <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}
