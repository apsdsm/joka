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
	// Removed are the file's `removed:` entries: state operations rather than
	// declarations. See Removal.
	Removed []Removal
}

// Removal is a declared state operation: an _id joka should stop tracking.
//
// Deleting an entity from a file already removes its row — the declaration is
// the desired state. What a removal adds is the other answer, Keep, which is
// the only way to stop owning a row without deleting it.
//
// It is a declaration rather than a command because the operation has to happen
// once per database, not once per operator. A command could only ever act on
// whichever database the person running it was pointed at; every other
// environment carried on tracking a row nobody meant to own. This is the same
// reasoning that moved terraform from imperative state surgery to `removed`
// blocks that live in the configuration and are applied by everyone.
//
// An entry that matches nothing does nothing, silently. That is what makes it
// safe to leave in the file until every environment has applied it — which is
// the author's judgement to make, so joka never suggests deleting one.
type Removal struct {
	// RefID is the _id to stop tracking.
	RefID string
	// Keep leaves the row in the database and drops only the tracking. Without
	// it the row is deleted, which is what an undeclared entity gets anyway.
	Keep bool
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
