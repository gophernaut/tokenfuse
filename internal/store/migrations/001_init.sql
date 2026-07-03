-- 001_init.sql
-- Core tables for TokenFuse. Idempotent where possible.

-- Usage samples (priced). Primary key makes re-polling the same window safe.
CREATE TABLE IF NOT EXISTS samples (
    provider        TEXT NOT NULL,
    key_id          TEXT NOT NULL,
    key_name        TEXT,
    model           TEXT NOT NULL,
    bucket_start    TEXT NOT NULL,   -- RFC3339
    uncached_input  INTEGER NOT NULL DEFAULT 0,
    cached_input    INTEGER NOT NULL DEFAULT 0,
    cache_creation  INTEGER NOT NULL DEFAULT 0,
    output          INTEGER NOT NULL DEFAULT 0,
    cost_usd        REAL NOT NULL DEFAULT 0,
    PRIMARY KEY (provider, key_id, model, bucket_start)
);

-- Breaker state. Survives restarts. Transitions are written before actions.
CREATE TABLE IF NOT EXISTS breaker_state (
    provider     TEXT NOT NULL,
    key_id       TEXT NOT NULL,
    state        TEXT NOT NULL CHECK (state IN ('closed','open','half_open')),
    tripped_at   TEXT,
    rule         TEXT,
    est_burn_usd REAL,
    PRIMARY KEY (provider, key_id)
);

-- Append-only audit log. Never deleted. Becomes the billing/export source later.
CREATE TABLE IF NOT EXISTS events (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    ts           TEXT NOT NULL,
    provider     TEXT,
    key_id       TEXT,
    kind         TEXT NOT NULL,
    detail_json  TEXT NOT NULL DEFAULT '{}'
);

-- Helpful indexes for common queries (status, detector, export)
CREATE INDEX IF NOT EXISTS idx_samples_key_day ON samples (provider, key_id, bucket_start);
CREATE INDEX IF NOT EXISTS idx_events_ts ON events (ts);
CREATE INDEX IF NOT EXISTS idx_events_key ON events (provider, key_id, ts);
