package infra

import (
	"context"
	"database/sql"
)

// DBTX is the minimal interface shared by *sql.DB and *sql.Tx, allowing the
// adapter to run queries against either a raw connection or inside a
// transaction.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}
