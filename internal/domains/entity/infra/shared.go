package infra

import (
	"context"
	"database/sql"
)

// DBTX is satisfied by both *sql.DB and *sql.Tx, allowing the adapter to
// operate inside or outside a transaction.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	// QueryContext is needed because the tracked-row reads have to see writes made
	// earlier in the same transaction: a run that re-points a row and then asks
	// which files still hold rows must not read the state from before it started.
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}
