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
	// Path is the key the state records this file under: relative to the
	// entities root, or root-prefixed when several roots are configured.
	Path string
	// FullPath is where the file is on disk. It is not part of the state — it
	// is carried so that reading the file again, or writing it back under
	// --on-conflict=ask, does not have to rebuild it by joining a root onto
	// Path, which stops working the moment Path carries a root of its own.
	FullPath    string
	ContentHash string
	Entities    []Entity
	// Removed are the file's `removed:` entries: state operations rather than
	// declarations. See Removal.
	Removed []Removal
	// Moved are the file's `moved:` entries. See Move.
	Moved []Move
	// Overrides are the file's `overrides:` entries: column values for an
	// entity declared elsewhere in the set. See Override.
	Overrides []Override
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

// Move is a declared state operation: the record under one _id becomes the
// record under another, and the row is untouched.
//
// Most renames need no declaration. When the natural key stays put, adoption
// finds the row again by it and sync infers the move. This is for the rename
// that also changes the unique key, which is indistinguishable from a delete
// plus an insert — joka cannot tell, and guessing either way would be wrong
// half the time.
//
// Like a Removal it is idempotent and silent once applied, because the same
// operation has to run once against every database.
type Move struct {
	// From is the _id joka currently tracks the row under.
	From string
	// To is the _id it should be tracked under, which some entity must declare.
	To string
}

// An Override sets column values on an entity declared elsewhere in the set.
//
//	overrides:
//	  - _id: lgc_client
//	    redirect_uri: https://test.example.com/callback
//
// It exists because one entity that differs in two fields per environment was
// duplicated whole — every column copied into every environment's tree, and
// every later edit made in each copy or forgotten in one. With several entity
// roots the shared declaration lives in one of them and each environment
// carries only what it changes.
//
// An override sets column values and nothing else. It cannot move an entity to
// another table, give it children or make a column seeded-once: those describe
// what the entity *is*, and an entity that is a different thing per environment
// is two entities.
type Override struct {
	// RefID is the _id being overridden. It must be declared somewhere in the
	// loaded set — an override naming nothing is a typo, and silently doing
	// nothing would leave the author believing an environment was configured.
	RefID string
	// Columns replace the declared values of the same name, and add the ones
	// the entity does not declare. Adding is allowed because "a field only
	// this environment needs" is the same request as "a field that differs";
	// a mistyped name fails at the insert, naming the column.
	Columns map[string]any
}
