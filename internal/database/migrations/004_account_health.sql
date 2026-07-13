ALTER TABLE accounts
    ADD COLUMN health_score DOUBLE PRECISION NOT NULL DEFAULT 1,
    ADD COLUMN failure_count INTEGER NOT NULL DEFAULT 0,
    ADD CONSTRAINT accounts_health_score_check CHECK (health_score >= 0 AND health_score <= 1),
    ADD CONSTRAINT accounts_failure_count_check CHECK (failure_count >= 0);
