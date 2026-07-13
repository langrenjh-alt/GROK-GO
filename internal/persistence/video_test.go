package persistence

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMediaIDFromPayload(t *testing.T) {
	payload := json.RawMessage(`{"id":"video_1","output":{"url":"https://gateway.test/media/AbCdEf_12345?exp=1&sig=x"}}`)
	if id := mediaIDFromPayload(payload); id != "AbCdEf_12345" {
		t.Fatalf("media ID = %q", id)
	}
	for _, invalid := range []json.RawMessage{
		json.RawMessage(`{"url":"https://gateway.test/other/AbCdEf_12345"}`),
		json.RawMessage(`{"url":"https://gateway.test/media/../../secret"}`),
		json.RawMessage(`not-json`),
	} {
		if id := mediaIDFromPayload(invalid); id != "" {
			t.Fatalf("unexpected media ID %q from %s", id, invalid)
		}
	}
}

func TestMediaIDFromPayloadPrefersVideoOverThumbnail(t *testing.T) {
	payload := json.RawMessage(`{
  "thumbnail_url":"https://gateway.test/media/image_thumb_123?sig=x",
  "url":"https://gateway.test/media/video_final_456?sig=y"
}`)
	for range 100 {
		if id := mediaIDFromPayload(payload); id != "video_final_456" {
			t.Fatalf("media ID = %q, want video_final_456", id)
		}
	}
}

func TestFailInterruptedVideos(t *testing.T) {
	databaseURL := os.Getenv("GROK_GO_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("integration database URL is not configured")
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `
		CREATE TEMP TABLE video_jobs (
			id text PRIMARY KEY,
			status text NOT NULL,
			payload jsonb NOT NULL,
			owner_id text NOT NULL DEFAULT '',
			updated_at timestamptz NOT NULL DEFAULT now()
		);
		INSERT INTO video_jobs (id, status, payload, owner_id, updated_at) VALUES
			('queued-job', 'queued', '{"id":"queued-job","status":"queued"}', 'instance-a', now()),
			('other-job', 'in_progress', '{"id":"other-job","status":"in_progress"}', 'instance-b', now()),
			('legacy-job', 'queued', '{"id":"legacy-job","status":"queued"}', '', now() - interval '1 hour'),
			('complete-job', 'completed', '{"id":"complete-job","status":"completed"}', 'instance-a', now())`); err != nil {
		t.Fatal(err)
	}

	count, err := (VideoStore{Pool: pool, OwnerID: "instance-a"}).FailInterruptedVideos(ctx, time.Now().Add(-15*time.Minute))
	if err != nil || count != 2 {
		t.Fatalf("FailInterruptedVideos() = %d, %v", count, err)
	}
	var status, code string
	if err := pool.QueryRow(ctx, `SELECT status, payload->'error'->>'code' FROM video_jobs WHERE id = 'queued-job'`).Scan(&status, &code); err != nil {
		t.Fatal(err)
	}
	if status != "failed" || code != "service_restarted" {
		t.Fatalf("interrupted job = status %q, code %q", status, code)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM video_jobs WHERE id = 'complete-job'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "completed" {
		t.Fatalf("completed job status = %q", status)
	}
	if err := pool.QueryRow(ctx, `SELECT status FROM video_jobs WHERE id = 'other-job'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "in_progress" {
		t.Fatalf("other instance job status = %q", status)
	}
}
