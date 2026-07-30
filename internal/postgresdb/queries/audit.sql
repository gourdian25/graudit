-- File: internal/postgresdb/queries/audit.sql

-- name: GetLastEntry :one
SELECT chain_id, entry_id, actor_id, entity_type, entity_id, action, payload, timestamp, hash, prev_hash
FROM graudit_entries WHERE chain_id = @chain_id ORDER BY entry_id DESC LIMIT 1;

-- name: GetEntryByID :one
SELECT chain_id, entry_id, actor_id, entity_type, entity_id, action, payload, timestamp, hash, prev_hash
FROM graudit_entries WHERE chain_id = @chain_id AND entry_id = @entry_id;

-- name: InsertEntry :exec
INSERT INTO graudit_entries (chain_id, entry_id, actor_id, entity_type, entity_id, action, payload, timestamp, hash, prev_hash)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10);

-- name: ListEntriesInRange :many
SELECT chain_id, entry_id, actor_id, entity_type, entity_id, action, payload, timestamp, hash, prev_hash
FROM graudit_entries
WHERE chain_id = @chain_id AND entry_id >= @from_id AND entry_id <= @to_id
ORDER BY entry_id ASC;

-- name: QueryEntries :many
SELECT chain_id, entry_id, actor_id, entity_type, entity_id, action, payload, timestamp, hash, prev_hash
FROM graudit_entries
WHERE chain_id = @chain_id
  AND (sqlc.narg('actor_id')::text IS NULL OR actor_id = sqlc.narg('actor_id'))
  AND (sqlc.narg('entity_type')::text IS NULL OR entity_type = sqlc.narg('entity_type'))
  AND (sqlc.narg('entity_id')::text IS NULL OR entity_id = sqlc.narg('entity_id'))
  AND (sqlc.narg('from_ts')::timestamptz IS NULL OR timestamp >= sqlc.narg('from_ts'))
  AND (sqlc.narg('to_ts')::timestamptz IS NULL OR timestamp <= sqlc.narg('to_ts'))
ORDER BY entry_id ASC
LIMIT sqlc.narg('lim')::bigint;
