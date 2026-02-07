package app

import "context"

type DBAdapter interface {
	TruncateTable(ctx context.Context, tableName string) error
	InsertRows(ctx context.Context, tableName string, rows []map[string]any) (int, error)
}
