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

	// Once names the columns joka sets when it inserts the row and never
	// writes again (from _once). A seed row has three kinds of column: one
	// joka owns, one joka seeds and then lets go of, and one joka never
	// touches because the file does not declare it. This is the middle kind.
	//
	// It exists because a user resetting their password is not a difference to
	// resolve, it is a column the application owns from then on. Without it
	// every sync of a modified file rewrites the password back, which is the
	// problem that made entity sync skip already-synced files in the first
	// place.
	Once []string
}

// IsOnce reports whether a column is seeded once and then left alone.
func (e Entity) IsOnce(column string) bool {
	for _, name := range e.Once {
		if name == column {
			return true
		}
	}
	return false
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
// The json tags are here because State.Unkeyed is serialised into the state
// document, and Go field names beside EntityState's lowercase ones would have
// made one document with two spellings.
type TrackedRow struct {
	EntityFile     string `json:"file"`
	TableName      string `json:"table"`
	RowPK          int64  `json:"pk_value"`
	PKColumn       string `json:"pk_column"`
	RefID          string `json:"ref_id,omitempty"`
	InsertionOrder int    `json:"order"`
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
