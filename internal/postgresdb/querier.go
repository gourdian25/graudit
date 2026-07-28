// File: internal/postgresdb/querier.go

// versions:
//   sqlc v1.31.1

package postgresdb

import (
	"context"
)

type Querier interface {
	// File: internal/postgresdb/queries/audit.sql
	GetLastEntry(ctx context.Context, chainID string) (GrauditEntry, error)
	InsertEntry(ctx context.Context, arg InsertEntryParams) error
	ListEntriesInRange(ctx context.Context, arg ListEntriesInRangeParams) ([]GrauditEntry, error)
	QueryEntries(ctx context.Context, arg QueryEntriesParams) ([]GrauditEntry, error)
}

var _ Querier = (*Queries)(nil)
