-- Generic credential handles: a short-lived, narrowly scoped, revocable
-- reference to a credential whose truth never enters a sandbox.
--
-- This generalizes the LLM-only llm_facade_token table to every credential
-- form (git, MCP, custom registry, ...). The handle records only the SHA-256 of
-- the bearer value, exactly like llm_facade_token.token_hash, so a database
-- dump never reveals a usable credential.
--
-- Identity is attribution, not secrecy: handle_id is a stable public name so
-- the injection path can select a handle without ever handling the raw value.
--
-- Timestamps are Unix milliseconds (see this directory's README): a new table
-- stores new business timestamps in milliseconds, while the released
-- second-based llm_facade_token columns are left untouched.
CREATE TABLE IF NOT EXISTS credential_handle (
    handle_id TEXT PRIMARY KEY,
    kind TEXT NOT NULL,
    token_hash TEXT NOT NULL,
    token_fingerprint TEXT NOT NULL,
    env_name TEXT NOT NULL DEFAULT '',
    sandbox_id TEXT NOT NULL DEFAULT '',
    run_id TEXT NOT NULL DEFAULT '',
    endpoint TEXT NOT NULL DEFAULT '',
    owners_json TEXT NOT NULL DEFAULT '[]',
    issued_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    revoked_at INTEGER NOT NULL DEFAULT 0
);

-- The raw bearer value is looked up by its hash on the hot path, and the hash
-- must be unique so two handles can never collide on the same credential.
CREATE UNIQUE INDEX IF NOT EXISTS idx_credential_handle_token_hash
    ON credential_handle(token_hash);

-- Revocation is per sandbox: releasing a sandbox releases every handle it was
-- granted, and the index keeps that sweep cheap.
CREATE INDEX IF NOT EXISTS idx_credential_handle_sandbox
    ON credential_handle(sandbox_id, revoked_at, expires_at);
