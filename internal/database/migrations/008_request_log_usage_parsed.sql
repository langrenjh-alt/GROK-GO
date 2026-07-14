ALTER TABLE request_logs
    ADD COLUMN usage_parsed BOOLEAN NOT NULL DEFAULT FALSE;

-- Preserve the strongest fact available for logs written before this column
-- existed. Genuine parsed zero-token reports remain unknown, not invented.
UPDATE request_logs
SET usage_parsed = TRUE
WHERE input_tokens <> 0 OR output_tokens <> 0 OR cached_tokens <> 0;
