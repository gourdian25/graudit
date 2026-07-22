// File: internal/postgresdb/models.go

// versions:
//   sqlc v1.31.1

package postgresdb

import (
	"github.com/jackc/pgx/v5/pgtype"
)

type GrauditEntry struct {
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
