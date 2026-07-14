ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS credential_fingerprint BYTEA;

CREATE UNIQUE INDEX IF NOT EXISTS accounts_credential_fingerprint_unique
    ON accounts (kind, credential_fingerprint)
    WHERE credential_fingerprint IS NOT NULL;
