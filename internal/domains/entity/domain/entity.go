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
	Path     string
	Entities []Entity
}
