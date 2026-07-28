-- File: internal/postgresdb/schema.sql

-- entry_id is NOT a BIGSERIAL/SERIAL column, deliberately: it is explicitly
-- assigned by application code inside the pg_advisory_xact_lock-held
-- transaction that inserts the row (see postgres.go). A Postgres sequence
-- advances even when its owning transaction rolls back, which would
-- silently create a gap in entry_id with no corresponding row — breaking
-- the "strictly increasing, no gaps" contract EntryID's own doc comment
-- states and making a legitimate rollback-gap indistinguishable from
-- tampering. See docs/architecture.md. This now applies per chain_id, not
-- globally: entry_id independently restarts at 1 in every chain, so the
-- primary key is the (chain_id, entry_id) pair, not entry_id alone.
CREATE TABLE IF NOT EXISTS graudit_entries (
    chain_id    TEXT NOT NULL,
    entry_id    BIGINT NOT NULL,
    actor_id    TEXT NOT NULL,
    entity_type TEXT NOT NULL,
    entity_id   TEXT NOT NULL,
    action      TEXT NOT NULL,
    payload     JSONB,
    timestamp   TIMESTAMPTZ NOT NULL,
    hash        CHAR(64) NOT NULL,
    prev_hash   CHAR(64) NOT NULL,
    PRIMARY KEY (chain_id, entry_id)
);

-- Global (not chain_id-scoped): chain_id is baked into every entry's Hash
-- preimage (see hash.go's ComputeHash), so a hash collision across two
-- different chains is already as astronomically unlikely as one within a
-- chain — no need for a composite index here.
CREATE UNIQUE INDEX IF NOT EXISTS idx_graudit_hash ON graudit_entries (hash);
CREATE INDEX IF NOT EXISTS idx_graudit_actor ON graudit_entries (chain_id, actor_id);
CREATE INDEX IF NOT EXISTS idx_graudit_entity ON graudit_entries (chain_id, entity_type, entity_id);
CREATE INDEX IF NOT EXISTS idx_graudit_timestamp ON graudit_entries (chain_id, timestamp);
