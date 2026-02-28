package app

import "context"

// DBAdapter abstracts the database operations needed by entity sync. The
// tracking methods (EnsureTrackingTable, IsEntitySynced, RecordEntitySynced)
// manage the joka_entities table. InsertRow performs a single INSERT and
// returns the auto-increment id for use in child entity references.
type DBAdapter interface {
	// EnsureTrackingTable creates the joka_entities table if it does not
	// already exist.
	EnsureTrackingTable(ctx context.Context) error

	// IsEntitySynced returns true if filePath has already been recorded in
	// the joka_entities table.
	IsEntitySynced(ctx context.Context, filePath string) (bool, error)

	// RecordEntitySynced inserts a row into joka_entities to mark filePath
	// as synced.
	RecordEntitySynced(ctx context.Context, filePath string) error

	// InsertRow inserts a single row into the given table and returns the
	// auto-generated primary key value. pkColumn identifies the primary key
	// column (e.g. "id") so the adapter can retrieve it portably.
	InsertRow(ctx context.Context, table string, columns map[string]any, pkColumn string) (int64, error)
}
