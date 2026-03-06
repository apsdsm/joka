package domain

// Entity represents a single database row within an object graph. It maps to
// one INSERT statement, where Table is the target table, RefID is an optional
// reference handle (from _id), PKColumn is the primary key column name (from
// _pk, defaults to "id"), Columns holds the column→value pairs, and Children
// contains nested entities that depend on this row's auto-increment id.
type Entity struct {
	Table    string
	RefID    string
	PKColumn string
	Columns  map[string]any
	Children []Entity
}

// EntityFile groups the entities parsed from a single YAML file. Path is the
// relative file path used as the tracking key in the joka_entities table.
type EntityFile struct {
	Path        string
	ContentHash string
	Entities    []Entity
}

// TrackedRow records a single row inserted during entity sync so it can be
// deleted later during reimport. InsertionOrder determines deletion order
// (highest first = children before parents).
type TrackedRow struct {
	EntityFile     string
	TableName      string
	RowPK          int64
	PKColumn       string
	RefID          string
	InsertionOrder int
}

// FileStatus represents the sync state of an entity file.
type FileStatus string

const (
	StatusSynced   FileStatus = "synced"
	StatusModified FileStatus = "modified"
	StatusNew      FileStatus = "new"
	StatusOrphaned FileStatus = "orphaned"
)

// EntityFileInfo pairs a file path with its sync status.
type EntityFileInfo struct {
	Path   string
	Status FileStatus
}
