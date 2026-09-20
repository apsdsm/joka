package app

import (
	"context"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// DBAdapter abstracts the database operations needed by entity sync. The
// tracking methods manage the joka_entities and joka_entity_rows tables.
// InsertRow performs a single INSERT and returns the auto-increment id for use
// in child entity references.
//
// Table creation is deliberately absent: it is the command layer's business,
// which calls EnsureTables on the concrete adapter once before handing it over.
// An action that could create the table it reads cannot be used by a read-only
// caller, which is what `joka status` needs.
type DBAdapter interface {
	// IsEntitySynced returns true if filePath has already been recorded in
	// the joka_entities table.
	IsEntitySynced(ctx context.Context, filePath string) (bool, error)

	// RecordEntitySyncedWithHash inserts a row into joka_entities with a
	// content hash for change detection.
	RecordEntitySyncedWithHash(ctx context.Context, filePath, contentHash string) error

	// UpdateEntitySynced updates an existing joka_entities row with a new
	// content hash and synced_at timestamp.
	UpdateEntitySynced(ctx context.Context, filePath, contentHash string) error

	// RecordEntityRow inserts a row into joka_entity_rows to track an
	// individual inserted entity row.
	RecordEntityRow(ctx context.Context, row domain.TrackedRow) error

	// GetAllTrackedRows returns every row in joka_entity_rows. Identity
	// matching is a property of the whole set — an entity can move between
	// files, so the row it corresponds to may be tracked against a file other
	// than the one now declaring it, which a per-file read cannot see.
	GetAllTrackedRows(ctx context.Context) ([]domain.TrackedRow, error)

	// RetrackEntityRow re-points a tracked row at the file and position now
	// declaring it. The row it identifies does not change.
	RetrackEntityRow(ctx context.Context, refID, entityFile string, insertionOrder int) error

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

	// TableExists reports whether the named table is present in the current
	// database/schema. Used by entity forget to distinguish a tracked row
	// whose table a later migration dropped from one that is still there —
	// querying the dropped table for the row would just error.
	TableExists(ctx context.Context, table string) (bool, error)

	// RowExists reports whether a single row is still present, matched by
	// pkColumn = pkValue. Used by entity forget to refuse to drop the
	// tracking for rows that are still in the database.
	RowExists(ctx context.Context, table, pkColumn string, pkValue int64) (bool, error)

	// DeleteEntityRecord removes the joka_entities row for a given file path.
	DeleteEntityRecord(ctx context.Context, filePath string) error

	// InsertRow inserts a single row into the given table and returns the
	// auto-generated primary key value. pkColumn identifies the primary key
	// column (e.g. "id") so the adapter can retrieve it portably.
	InsertRow(ctx context.Context, table string, columns map[string]any, pkColumn string) (int64, error)

	// UpdateRow updates a single existing row in the given table, matched by
	// pkColumn = pkValue, setting every column in the columns map (the
	// pkColumn itself is never written). Used by entity sync to propagate
	// field-level changes to [modified] files without deleting the row.
	UpdateRow(ctx context.Context, table, pkColumn string, pkValue int64, columns map[string]any) error

	// GetRow reads the given columns from a single row, matched by
	// pkColumn = pkValue, and returns them as a column→value map. Used by the
	// sync preview to show the "before" side of an update. Byte-slice values
	// are converted to strings for comparison. Returns an error if the row
	// does not exist.
	GetRow(ctx context.Context, table string, columns []string, pkColumn string, pkValue int64) (map[string]any, error)

	// LookupValue queries a single value from an existing table row. Used by
	// {{ lookup|table,return_col,where_col=value }} template expressions to
	// resolve foreign keys against data seeded outside the entity file.
	LookupValue(ctx context.Context, table, returnCol, whereCol string, whereVal any) (any, error)
}
