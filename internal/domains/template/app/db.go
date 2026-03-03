package app

import "context"

type DBAdapter interface {
	TruncateTable(ctx context.Context, tableName string) error
	InsertRows(ctx context.Context, tableName string, rows []map[string]any) (int, error)

	// DisableForeignKeys temporarily disables FK constraint checking for the
	// current session/transaction. EnableForeignKeys re-enables it. Used by
	// the --ignore-foreign-keys flag to allow TRUNCATE on referenced tables.
	DisableForeignKeys(ctx context.Context) error
	EnableForeignKeys(ctx context.Context) error
}
