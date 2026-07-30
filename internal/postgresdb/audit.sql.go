// File: internal/postgresdb/audit.sql.go

// versions:
//   sqlc v1.31.1
// source: audit.sql

package postgresdb

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

const getEntryByID = `-- name: GetEntryByID :one
SELECT chain_id, entry_id, actor_id, entity_type, entity_id, action, payload, timestamp, hash, prev_hash
FROM graudit_entries WHERE chain_id = $1 AND entry_id = $2
`

type GetEntryByIDParams struct {
	ChainID string `db:"chain_id" json:"chain_id"`
	EntryID int64  `db:"entry_id" json:"entry_id"`
}

func (q *Queries) GetEntryByID(ctx context.Context, arg GetEntryByIDParams) (GrauditEntry, error) {
	row := q.db.QueryRow(ctx, getEntryByID, arg.ChainID, arg.EntryID)
	var i GrauditEntry
	err := row.Scan(
		&i.ChainID,
		&i.EntryID,
		&i.ActorID,
		&i.EntityType,
		&i.EntityID,
		&i.Action,
		&i.Payload,
		&i.Timestamp,
		&i.Hash,
		&i.PrevHash,
	)
	return i, err
}

const getLastEntry = `-- name: GetLastEntry :one

SELECT chain_id, entry_id, actor_id, entity_type, entity_id, action, payload, timestamp, hash, prev_hash
FROM graudit_entries WHERE chain_id = $1 ORDER BY entry_id DESC LIMIT 1
`

// File: internal/postgresdb/queries/audit.sql
func (q *Queries) GetLastEntry(ctx context.Context, chainID string) (GrauditEntry, error) {
	row := q.db.QueryRow(ctx, getLastEntry, chainID)
	var i GrauditEntry
	err := row.Scan(
		&i.ChainID,
		&i.EntryID,
		&i.ActorID,
		&i.EntityType,
		&i.EntityID,
		&i.Action,
		&i.Payload,
		&i.Timestamp,
		&i.Hash,
		&i.PrevHash,
	)
	return i, err
}

const insertEntry = `-- name: InsertEntry :exec
INSERT INTO graudit_entries (chain_id, entry_id, actor_id, entity_type, entity_id, action, payload, timestamp, hash, prev_hash)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
`

type InsertEntryParams struct {
	ChainID    string             `db:"chain_id" json:"chain_id"`
	EntryID    int64              `db:"entry_id" json:"entry_id"`
	ActorID    string             `db:"actor_id" json:"actor_id"`
	EntityType string             `db:"entity_type" json:"entity_type"`
	EntityID   string             `db:"entity_id" json:"entity_id"`
	Action     string             `db:"action" json:"action"`
	Payload    []byte             `db:"payload" json:"payload"`
	Timestamp  pgtype.Timestamptz `db:"timestamp" json:"timestamp"`
	Hash       string             `db:"hash" json:"hash"`
	PrevHash   string             `db:"prev_hash" json:"prev_hash"`
}

func (q *Queries) InsertEntry(ctx context.Context, arg InsertEntryParams) error {
	_, err := q.db.Exec(ctx, insertEntry,
		arg.ChainID,
		arg.EntryID,
		arg.ActorID,
		arg.EntityType,
		arg.EntityID,
		arg.Action,
		arg.Payload,
		arg.Timestamp,
		arg.Hash,
		arg.PrevHash,
	)
	return err
}

const listEntriesInRange = `-- name: ListEntriesInRange :many
SELECT chain_id, entry_id, actor_id, entity_type, entity_id, action, payload, timestamp, hash, prev_hash
FROM graudit_entries
WHERE chain_id = $1 AND entry_id >= $2 AND entry_id <= $3
ORDER BY entry_id ASC
`

type ListEntriesInRangeParams struct {
	ChainID string `db:"chain_id" json:"chain_id"`
	FromID  int64  `db:"from_id" json:"from_id"`
	ToID    int64  `db:"to_id" json:"to_id"`
}

func (q *Queries) ListEntriesInRange(ctx context.Context, arg ListEntriesInRangeParams) ([]GrauditEntry, error) {
	rows, err := q.db.Query(ctx, listEntriesInRange, arg.ChainID, arg.FromID, arg.ToID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []GrauditEntry
	for rows.Next() {
		var i GrauditEntry
		if err := rows.Scan(
			&i.ChainID,
			&i.EntryID,
			&i.ActorID,
			&i.EntityType,
			&i.EntityID,
			&i.Action,
			&i.Payload,
			&i.Timestamp,
			&i.Hash,
			&i.PrevHash,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

const queryEntries = `-- name: QueryEntries :many
SELECT chain_id, entry_id, actor_id, entity_type, entity_id, action, payload, timestamp, hash, prev_hash
FROM graudit_entries
WHERE chain_id = $1
  AND ($2::text IS NULL OR actor_id = $2)
  AND ($3::text IS NULL OR entity_type = $3)
  AND ($4::text IS NULL OR entity_id = $4)
  AND ($5::timestamptz IS NULL OR timestamp >= $5)
  AND ($6::timestamptz IS NULL OR timestamp <= $6)
ORDER BY entry_id ASC
LIMIT $7::bigint
`

type QueryEntriesParams struct {
	ChainID    string             `db:"chain_id" json:"chain_id"`
	ActorID    pgtype.Text        `db:"actor_id" json:"actor_id"`
	EntityType pgtype.Text        `db:"entity_type" json:"entity_type"`
	EntityID   pgtype.Text        `db:"entity_id" json:"entity_id"`
	FromTs     pgtype.Timestamptz `db:"from_ts" json:"from_ts"`
	ToTs       pgtype.Timestamptz `db:"to_ts" json:"to_ts"`
	Lim        pgtype.Int8        `db:"lim" json:"lim"`
}

func (q *Queries) QueryEntries(ctx context.Context, arg QueryEntriesParams) ([]GrauditEntry, error) {
	rows, err := q.db.Query(ctx, queryEntries,
		arg.ChainID,
		arg.ActorID,
		arg.EntityType,
		arg.EntityID,
		arg.FromTs,
		arg.ToTs,
		arg.Lim,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []GrauditEntry
	for rows.Next() {
		var i GrauditEntry
		if err := rows.Scan(
			&i.ChainID,
			&i.EntryID,
			&i.ActorID,
			&i.EntityType,
			&i.EntityID,
			&i.Action,
			&i.Payload,
			&i.Timestamp,
			&i.Hash,
			&i.PrevHash,
		); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
