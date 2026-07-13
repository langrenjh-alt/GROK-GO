package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/langrenjh-alt/GROK-GO/internal/accounts"
	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/upstream"
)

var ErrVideoNotFound = errors.New("video job not found")

type VideoJob struct {
	ID        string          `json:"id"`
	AccountID string          `json:"-"`
	Status    string          `json:"status"`
	StatusURL string          `json:"-"`
	Payload   json.RawMessage `json:"-"`
}

// VideoStore is deliberately independent of the account/model repositories.
// A production implementation can persist jobs in SQL and content in media.Store.
type VideoStore interface {
	SaveVideo(context.Context, VideoJob) error
	GetVideo(context.Context, string) (VideoJob, error)
	OpenVideoContent(context.Context, string) (domain.MediaObject, io.ReadCloser, error)
}

type MemoryVideoStore struct {
	mu      sync.RWMutex
	jobs    map[string]VideoJob
	content map[string]memoryVideoContent
}

type memoryVideoContent struct {
	object domain.MediaObject
	data   []byte
}

func NewMemoryVideoStore() *MemoryVideoStore {
	return &MemoryVideoStore{jobs: make(map[string]VideoJob), content: make(map[string]memoryVideoContent)}
}

func (s *MemoryVideoStore) SaveVideo(_ context.Context, job VideoJob) error {
	if strings.TrimSpace(job.ID) == "" {
		return errors.New("video job ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job.Payload = append(json.RawMessage(nil), job.Payload...)
	s.jobs[job.ID] = job
	return nil
}

func (s *MemoryVideoStore) GetVideo(_ context.Context, id string) (VideoJob, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job, ok := s.jobs[id]
	if !ok {
		return VideoJob{}, ErrVideoNotFound
	}
	job.Payload = append(json.RawMessage(nil), job.Payload...)
	return job, nil
}

func (s *MemoryVideoStore) OpenVideoContent(_ context.Context, id string) (domain.MediaObject, io.ReadCloser, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.content[id]
	if !ok {
		return domain.MediaObject{}, nil, ErrVideoNotFound
	}
	return item.object, io.NopCloser(strings.NewReader(string(item.data))), nil
}

func (s *MemoryVideoStore) SetVideoContent(id string, object domain.MediaObject, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.content[id] = memoryVideoContent{object: object, data: append([]byte(nil), data...)}
}

func (h *Handler) videoGet(w http.ResponseWriter, r *http.Request, suffix string) {
	if h.config.Videos == nil {
		writeError(w, http.StatusNotFound, "Video job not found", "invalid_request_error", "video_not_found", "video_id")
		return
	}
	content := strings.HasSuffix(suffix, "/content")
	id := strings.TrimSuffix(suffix, "/content")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusBadRequest, "Invalid video ID", "invalid_request_error", "invalid_value", "video_id")
		return
	}
	if content {
		object, reader, err := h.config.Videos.OpenVideoContent(r.Context(), id)
		if err != nil {
			writeError(w, http.StatusNotFound, "Video content not found", "invalid_request_error", "video_not_found", "video_id")
			return
		}
		defer reader.Close()
		contentType := object.ContentType
		if contentType == "" {
			contentType = "video/mp4"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", `inline; filename="`+id+`.mp4"`)
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, reader)
		return
	}
	job, err := h.config.Videos.GetVideo(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "Video job not found", "invalid_request_error", "video_not_found", "video_id")
		return
	}
	if json.Valid(job.Payload) && len(job.Payload) > 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(job.Payload)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": job.ID, "object": "video", "status": job.Status})
}

func saveVideoPayload(ctx context.Context, store VideoStore, payload json.RawMessage) {
	if store == nil || !json.Valid(payload) {
		return
	}
	job, ok := videoJobFromPayload(payload, "")
	if !ok {
		return
	}
	_ = store.SaveVideo(ctx, job)
}

func (h *Handler) saveVideoAndStartPolling(ctx context.Context, payload json.RawMessage, lease *accounts.Lease) bool {
	// Background polling must be owned by an explicit service lifecycle. A
	// standalone handler still persists queued metadata and releases its lease.
	if h.config.BackgroundContext == nil || h.config.Videos == nil || lease == nil || !json.Valid(payload) {
		return false
	}
	job, ok := videoJobFromPayload(payload, "")
	if !ok {
		return false
	}
	job.AccountID = lease.Account.ID
	if err := h.config.Videos.SaveVideo(ctx, job); err != nil || videoStatusTerminal(job.Status) {
		return false
	}
	pollContext, cancel := context.WithTimeout(h.config.BackgroundContext, h.config.VideoPollTimeout)
	go h.pollVideo(pollContext, cancel, job, lease)
	return true
}

func (h *Handler) pollVideo(ctx context.Context, cancel context.CancelFunc, job VideoJob, lease *accounts.Lease) {
	defer cancel()
	feedback := accounts.Feedback{StatusCode: http.StatusOK}
	defer func() { _ = lease.Release(context.Background(), feedback) }()

	proxyURL := ""
	if lease.Account.ProxyID != "" && h.config.ProxyURL != nil {
		resolved, err := h.config.ProxyURL(ctx, lease.Account.ProxyID)
		if err != nil {
			feedback = accounts.Feedback{StatusCode: http.StatusBadGateway, Err: err}
			h.failVideoPolling(job, "video_poll_proxy_error", "Failed to resolve the video account proxy")
			return
		}
		proxyURL = resolved
	}

	timer := time.NewTimer(h.config.VideoPollInterval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				feedback = accounts.Feedback{StatusCode: http.StatusGatewayTimeout, Err: ctx.Err()}
				h.failVideoPolling(job, "video_poll_timeout", "Video generation status polling timed out")
			}
			return
		case <-timer.C:
		}

		statusURL := ""
		if lease.Account.Kind == domain.CredentialGrokSSO {
			statusURL = job.StatusURL
		}
		response, err := h.config.Upstream.Do(ctx, upstream.Request{
			Operation:      upstream.OperationVideoStatus,
			CredentialKind: lease.Account.Kind,
			Credentials:    lease.Credentials,
			ProxyURL:       proxyURL,
			VideoID:        job.ID,
			StatusURL:      statusURL,
		})
		if err != nil {
			if ctx.Err() != nil {
				continue
			}
			timer.Reset(h.config.VideoPollInterval)
			continue
		}
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			if response.StatusCode >= http.StatusBadRequest && response.StatusCode < http.StatusInternalServerError && response.StatusCode != http.StatusNotFound && response.StatusCode != http.StatusTooManyRequests {
				feedback = accounts.Feedback{StatusCode: response.StatusCode, Err: fmt.Errorf("video status query returned %d", response.StatusCode)}
				h.failVideoPolling(job, "video_poll_upstream_error", "Video status query was rejected by the upstream")
				return
			}
			timer.Reset(videoPollDelay(h.config.VideoPollInterval, response.Header))
			continue
		}
		if !json.Valid(response.Body) {
			timer.Reset(h.config.VideoPollInterval)
			continue
		}
		localized, err := h.localizeBody(ctx, upstream.OperationVideoStatus, response.Body)
		if err != nil {
			if ctx.Err() != nil {
				continue
			}
			timer.Reset(h.config.VideoPollInterval)
			continue
		}
		next, ok := videoJobFromPayload(localized, job.ID)
		if !ok {
			timer.Reset(h.config.VideoPollInterval)
			continue
		}
		next.ID = job.ID
		next.AccountID = job.AccountID
		if next.StatusURL == "" {
			next.StatusURL = job.StatusURL
		}
		if err := h.config.Videos.SaveVideo(ctx, next); err != nil {
			timer.Reset(h.config.VideoPollInterval)
			continue
		}
		job = next
		if videoStatusTerminal(job.Status) {
			if videoStatusFailed(job.Status) {
				feedback = videoFailureFeedback(job.Payload)
			}
			return
		}
		timer.Reset(videoPollDelay(h.config.VideoPollInterval, response.Header))
	}
}

func (h *Handler) failVideoPolling(job VideoJob, code, message string) {
	if h.config.Videos == nil {
		return
	}
	var payload map[string]any
	if json.Unmarshal(job.Payload, &payload) != nil {
		payload = make(map[string]any)
	}
	payload["id"] = job.ID
	payload["status"] = "failed"
	payload["error"] = map[string]any{"code": code, "message": message}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	job.Status = "failed"
	job.Payload = encoded
	_ = h.config.Videos.SaveVideo(context.Background(), job)
}

func videoJobFromPayload(payload json.RawMessage, fallbackID string) (VideoJob, bool) {
	var root map[string]any
	if json.Unmarshal(payload, &root) != nil {
		return VideoJob{}, false
	}
	candidates := []map[string]any{root}
	for index := 0; index < len(candidates); index++ {
		for _, key := range []string{"data", "video", "result"} {
			if nested, ok := candidates[index][key].(map[string]any); ok {
				candidates = append(candidates, nested)
			}
		}
	}
	job := VideoJob{ID: strings.TrimSpace(fallbackID), Status: "queued", Payload: append(json.RawMessage(nil), payload...)}
	for _, candidate := range candidates {
		if job.ID == "" {
			job.ID = videoString(candidate, "request_id", "requestId", "id", "video_id", "videoId")
		}
		if status := videoString(candidate, "status", "state"); status != "" {
			job.Status = strings.ToLower(status)
		}
		if job.StatusURL == "" {
			job.StatusURL = videoString(candidate, "status_url", "statusUrl", "poll_url", "pollUrl")
		}
	}
	return job, job.ID != ""
}

func videoString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func videoStatusTerminal(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "succeeded", "done", "failed", "error", "cancelled", "canceled", "expired":
		return true
	default:
		return false
	}
}

func videoStatusFailed(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "error", "cancelled", "canceled", "expired":
		return true
	default:
		return false
	}
}

func videoFailureFeedback(payload json.RawMessage) accounts.Feedback {
	status := http.StatusBadGateway
	message := "video generation failed"
	var root map[string]any
	if json.Unmarshal(payload, &root) == nil {
		if failure, ok := root["error"].(map[string]any); ok {
			if value, ok := failure["message"].(string); ok && strings.TrimSpace(value) != "" {
				message = strings.TrimSpace(value)
			}
			switch value := failure["upstream_status"].(type) {
			case float64:
				if value >= 400 && value <= 599 {
					status = int(value)
				}
			case json.Number:
				if parsed, err := value.Int64(); err == nil && parsed >= 400 && parsed <= 599 {
					status = int(parsed)
				}
			}
		}
	}
	return accounts.Feedback{StatusCode: status, Err: errors.New(message)}
}

func videoPollDelay(fallback time.Duration, header http.Header) time.Duration {
	delay := retryAfter(header)
	if delay < fallback {
		return fallback
	}
	if delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
}
