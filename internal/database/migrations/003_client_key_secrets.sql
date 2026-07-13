ALTER TABLE client_keys
    ADD COLUMN IF NOT EXISTS secret_cipher BYTEA;
