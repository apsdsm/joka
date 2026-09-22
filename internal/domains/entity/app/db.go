package app

import "context"

// DBAdapter is what the entity actions do to the database itself: insert,
// update, delete and read the seeded rows, and answer whether a table or a row
// is still there.
//
// Nothing here concerns tracking. What joka last applied is one value behind
// StateBackend, loaded and saved whole, which is why this interface no longer
// carries IsEntitySynced, RecordEntityRow, RetrackEntityRow and the rest — nine
// methods that existed only because six commands each reached for the tracking
// columns they happened to need.
//
// Table creation is deliberately absent too: it is the command layer's
// business, which calls EnsureTables on the concrete adapter once before
// handing it over. An action that could create the table it reads cannot be
// used by a read-only caller, which is what `joka status` needs.
type DBAdapter interface {
	// InsertRow inserts a single row into the given table and returns the
	// auto-generated primary key value. pkColumn identifies the primary key
	// column (e.g. "id") so the adapter can retrieve it portably.
	InsertRow(ctx context.Context, table string, columns map[string]any, pkColumn string) (int64, error)

	// UpdateRow updates a single existing row in the given table, matched by
	// pkColumn = pkValue, setting every column in the columns map (the
	// pkColumn itself is never written). Used by entity sync to propagate
	// field-level changes to [modified] files without deleting the row.
	UpdateRow(ctx context.Context, table, pkColumn string, pkValue int64, columns map[string]any) error

	// DeleteRow deletes a single row from the given table by primary key.
	// Returns an error wrapping ErrForeignKeyConflict if a FK constraint
	// blocks the deletion.
	//
	// Sync uses it for a tracked entity no file declares any more. Nothing else
	// deletes: this is the only path by which joka removes a seeded row.
	DeleteRow(ctx context.Context, table, pkColumn string, pkValue int64) error

	// GetRow reads the given columns from a single row, matched by
	// pkColumn = pkValue, and returns them as a column→value map. Used by the
	// sync preview to show the "before" side of an update. Byte-slice values
	// are converted to strings for comparison. Returns an error if the row
	// does not exist.
	GetRow(ctx context.Context, table string, columns []string, pkColumn string, pkValue int64) (map[string]any, error)

	// TableExists reports whether the named table is present in the current
	// database/schema. Used by entity forget to distinguish a tracked row
	// whose table a later migration dropped from one that is still there —
	// querying the dropped table for the row would just error.
	TableExists(ctx context.Context, table string) (bool, error)

	// RowExists reports whether a single row is still present, matched by
	// pkColumn = pkValue. Used by entity forget to refuse to drop the
	// tracking for rows that are still in the database.
	RowExists(ctx context.Context, table, pkColumn string, pkValue int64) (bool, error)

	// LookupValue queries a single value from an existing table row. Used by
	// {{ lookup|table,return_col,where_col=value }} template expressions to
	// resolve foreign keys against data seeded outside the entity file.
	LookupValue(ctx context.Context, table, returnCol, whereCol string, whereVal any) (any, error)

	// FindByUniqueKey returns the primary key of the row whose named columns
	// hold these values, or ErrRowNotFound. The columns come from UniqueKeys,
	// so at most one row can match.
	FindByUniqueKey(ctx context.Context, table, pkColumn string, key map[string]any) (int64, error)

	// UniqueKeys returns the table's unique indexes as column lists, each
	// usable on its own to identify one row. Adoption uses them to find the row
	// a declared entity already corresponds to in a database joka does not track
	// yet.
	//
	// The primary key is included — it is a unique index, and an entity that
	// declares it can be adopted by it. Partial and expression indexes are left
	// out: neither identifies a row by the values an entity declares.
	UniqueKeys(ctx context.Context, table string) ([][]string, error)
}
