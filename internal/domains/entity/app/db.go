package app

import (
	"context"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// DBAdapter abstracts the database operations needed by entity sync. The
// tracking methods (EnsureTrackingTable, IsEntitySynced, RecordEntitySynced)
// manage the joka_entities table. InsertRow performs a single INSERT and
// returns the auto-increment id for use in child entity references.
type DBAdapter interface {
	// EnsureTrackingTable creates the joka_entities table if it does not
	// already exist.
	EnsureTrackingTable(ctx context.Context) error

	// EnsureRowTrackingTable creates the joka_entity_rows table if it does
	// not already exist. This table tracks individual rows inserted per
	// entity file for reimport support.
	EnsureRowTrackingTable(ctx context.Context) error

	// EnsureContentHashColumn adds the content_hash column to joka_entities
	// if it is not already present.
	EnsureContentHashColumn(ctx context.Context) error

	// IsEntitySynced returns true if filePath has already been recorded in
	// the joka_entities table.
	IsEntitySynced(ctx context.Context, filePath string) (bool, error)

	// RecordEntitySynced inserts a row into joka_entities to mark filePath
	// as synced.
	RecordEntitySynced(ctx context.Context, filePath string) error

	// RecordEntitySyncedWithHash inserts a row into joka_entities with a
	// content hash for change detection.
	RecordEntitySyncedWithHash(ctx context.Context, filePath, contentHash string) error

	// UpdateEntitySynced updates an existing joka_entities row with a new
	// content hash and synced_at timestamp.
	UpdateEntitySynced(ctx context.Context, filePath, contentHash string) error

	// GetEntityHash returns the content_hash stored for a synced entity file.
	// Returns empty string if no hash is stored or the file is not found.
	GetEntityHash(ctx context.Context, filePath string) (string, error)

	// GetAllSyncedEntities returns all entity_file paths from joka_entities
	// mapped to their content hashes. NULL hashes are returned as empty strings.
	GetAllSyncedEntities(ctx context.Context) (map[string]string, error)

	// RecordEntityRow inserts a row into joka_entity_rows to track an
	// individual inserted entity row.
	RecordEntityRow(ctx context.Context, row domain.TrackedRow) error

	// GetTrackedRows returns all rows from joka_entity_rows for a given
	// entity file, ordered by insertion_order DESC (for reverse deletion).
	GetTrackedRows(ctx context.Context, entityFile string) ([]domain.TrackedRow, error)

	// DeleteTrackedRows removes all joka_entity_rows entries for a given
	// entity file.
	DeleteTrackedRows(ctx context.Context, entityFile string) error

	// DeleteRow deletes a single row from the given table by primary key.
	// Returns an error wrapping ErrForeignKeyConflict if a FK constraint
	// blocks the deletion.
	DeleteRow(ctx context.Context, table, pkColumn string, pkValue int64) error

	// DeleteEntityRecord removes the joka_entities row for a given file path.
	DeleteEntityRecord(ctx context.Context, filePath string) error

	// InsertRow inserts a single row into the given table and returns the
	// auto-generated primary key value. pkColumn identifies the primary key
	// column (e.g. "id") so the adapter can retrieve it portably.
	InsertRow(ctx context.Context, table string, columns map[string]any, pkColumn string) (int64, error)

	// LookupValue queries a single value from an existing table row. Used by
	// {{ lookup|table,return_col,where_col=value }} template expressions to
	// resolve foreign keys against data seeded outside the entity file.
	LookupValue(ctx context.Context, table, returnCol, whereCol string, whereVal any) (any, error)
}
