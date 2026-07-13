ALTER TABLE video_jobs
    ADD COLUMN IF NOT EXISTS owner_id TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS video_jobs_owner_status_idx
    ON video_jobs (owner_id, status, updated_at);
