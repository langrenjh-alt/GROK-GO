package persistence

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/langrenjh-alt/GROK-GO/internal/domain"
	"github.com/langrenjh-alt/GROK-GO/internal/gateway"
	"github.com/langrenjh-alt/GROK-GO/internal/media"
)

type VideoStore struct {
	Pool    *pgxpool.Pool
	Media   *media.FileStore
	OwnerID string
}

// FailInterruptedVideos closes jobs whose generation goroutine belonged to a
// previous process. Grok's segmented generation cannot be resumed safely after
// credentials and in-memory upstream state have been lost.
func (s VideoStore) FailInterruptedVideos(ctx context.Context, legacyBefore time.Time) (int64, error) {
	if s.Pool == nil {
		return 0, errors.New("video database is not configured")
	}
	ownerID := strings.TrimSpace(s.OwnerID)
	if ownerID == "" {
		return 0, errors.New("video owner ID is not configured")
	}
	errorPayload := `{"code":"service_restarted","message":"Video generation was interrupted by a service restart."}`
	tag, err := s.Pool.Exec(ctx, `
		UPDATE video_jobs
		SET status = 'failed',
			payload = (CASE WHEN jsonb_typeof(payload) = 'object' THEN payload ELSE '{}'::jsonb END)
				|| jsonb_build_object('status', 'failed', 'error', $1::jsonb),
			updated_at = now()
		WHERE lower(status) IN ('queued', 'in_progress', 'processing', 'running')
			AND (owner_id = $2 OR (owner_id = '' AND updated_at < $3))`, errorPayload, ownerID, legacyBefore.UTC())
	if err != nil {
		return 0, fmt.Errorf("fail interrupted video jobs: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (s VideoStore) SaveVideo(ctx context.Context, job gateway.VideoJob) error {
	if s.Pool == nil {
		return errors.New("video database is not configured")
	}
	mediaID := mediaIDFromPayload(job.Payload)
	_, err := s.Pool.Exec(ctx, `
		INSERT INTO video_jobs (id, account_id, status, payload, media_id, owner_id, updated_at)
		VALUES ($1, NULLIF($2, ''), $3, $4::jsonb, NULLIF($5, ''), $6, now())
		ON CONFLICT (id) DO UPDATE SET status = EXCLUDED.status,
			account_id = COALESCE(EXCLUDED.account_id, video_jobs.account_id),
			payload = EXCLUDED.payload,
			media_id = COALESCE(EXCLUDED.media_id, video_jobs.media_id),
			owner_id = CASE WHEN EXCLUDED.owner_id <> '' THEN EXCLUDED.owner_id ELSE video_jobs.owner_id END,
			updated_at = now()`, job.ID, job.AccountID, job.Status, job.Payload, mediaID, strings.TrimSpace(s.OwnerID))
	return err
}

func (s VideoStore) GetVideo(ctx context.Context, id string) (gateway.VideoJob, error) {
	if s.Pool == nil {
		return gateway.VideoJob{}, errors.New("video database is not configured")
	}
	var job gateway.VideoJob
	err := s.Pool.QueryRow(ctx, `SELECT id, COALESCE(account_id, ''), status, payload FROM video_jobs WHERE id = $1`, id).
		Scan(&job.ID, &job.AccountID, &job.Status, &job.Payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return gateway.VideoJob{}, gateway.ErrVideoNotFound
	}
	return job, err
}

func (s VideoStore) OpenVideoContent(ctx context.Context, id string) (domain.MediaObject, io.ReadCloser, error) {
	if s.Pool == nil || s.Media == nil {
		return domain.MediaObject{}, nil, gateway.ErrVideoNotFound
	}
	var mediaID string
	err := s.Pool.QueryRow(ctx, `SELECT media_id FROM video_jobs WHERE id = $1 AND media_id IS NOT NULL`, id).Scan(&mediaID)
	if err != nil {
		return domain.MediaObject{}, nil, gateway.ErrVideoNotFound
	}
	object, reader, err := s.Media.Open(ctx, mediaID)
	if err != nil {
		return domain.MediaObject{}, nil, gateway.ErrVideoNotFound
	}
	return object, reader, nil
}

func mediaIDFromPayload(payload json.RawMessage) string {
	var value any
	if json.Unmarshal(payload, &value) != nil {
		return ""
	}
	return prioritizedMediaID(value)
}

func prioritizedMediaID(value any) string {
	switch current := value.(type) {
	case map[string]any:
		for _, key := range []string{"url", "video_url", "videoUrl", "content_url", "contentUrl"} {
			if id := mediaIDFromURL(current[key]); id != "" {
				return id
			}
		}
		for _, key := range []string{"video", "data", "result", "output"} {
			if id := prioritizedMediaID(current[key]); id != "" {
				return id
			}
		}
		for key, child := range current {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "thumbnail") || strings.Contains(lower, "image") {
				continue
			}
			if id := prioritizedMediaID(child); id != "" {
				return id
			}
		}
	case []any:
		for _, child := range current {
			if id := prioritizedMediaID(child); id != "" {
				return id
			}
		}
	case string:
		return mediaIDFromURL(current)
	}
	return ""
}

func mediaIDFromURL(value any) string {
	raw, _ := value.(string)
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	for index := 0; index+1 < len(parts); index++ {
		if parts[index] == "media" && validMediaID(parts[index+1]) {
			return parts[index+1]
		}
	}
	return ""
}

func validMediaID(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_' {
			continue
		}
		return false
	}
	return true
}
