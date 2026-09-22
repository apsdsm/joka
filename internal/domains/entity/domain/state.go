package domain

import "sort"

// StateVersion is the shape of the state document this build reads and writes.
//
// 1 — files keyed by path with a content hash, entities keyed by _id with the
// table, primary key and the file that last declared them. This is the shape
// joka_entities and joka_entity_rows already held; version 1 names it rather
// than changing it.
//
// 2 — each entity also carries a per-column baseline: the SHA-256 of every
// value joka last applied. Additive, and an older joka ignores it harmlessly,
// which is why meta.TrackingVersion does not move with it.
const StateVersion = 2

// State is what joka last applied to one database.
//
// It exists because the tracking tables were never one artifact: their shape
// and meaning were spread across six commands, each reading the columns it
// happened to need, which is how three undocumented format changes got in. One
// value with one schema can be versioned, serialized, constructed in a test and
// compared against — none of which a set of queries can.
//
// Nothing here is keyed on the file. An entity can move between files, so the
// file is metadata recording where it was last declared; the _id is what
// identifies it.
type State struct {
	Version int `json:"version"`

	// Files is every entity file joka has synced, keyed by its path relative
	// to the entities directory.
	Files map[string]FileState `json:"files"`

	// Entities is every row joka has written, keyed by the _id that declared
	// it.
	Entities map[string]EntityState `json:"entities"`

	// Unkeyed are tracked rows with no _id, written before joka recorded one.
	// They cannot go in Entities because there is nothing to key them on, and
	// dropping them would lose the rows they point at. Every one of them is a
	// blocker for tracking version 2, which is where they get resolved; until
	// then they are carried so a reader can report them.
	Unkeyed []TrackedRow `json:"unkeyed,omitempty"`
}

// FileState is what joka recorded about one entity file.
type FileState struct {
	// ContentHash is the SHA-256 of the file as last applied. Empty for a file
	// synced before content hashing existed, which reads as modified.
	ContentHash string `json:"content_hash"`
}

// EntityState is the row one _id became.
type EntityState struct {
	Table    string `json:"table"`
	PKColumn string `json:"pk_column"`
	PKValue  int64  `json:"pk_value"`

	// File is where the entity was last declared. Metadata: it is what error
	// messages and `entity diff` need in order to say where a row came from,
	// and nothing matches on it.
	File string `json:"file"`

	// Order is the entity's position in the depth-first walk of the file that
	// declared it. It survives for one reason: reimport deletes a file's rows
	// in reverse order so children go before parents, and a foreign key makes
	// that ordering load-bearing.
	Order int `json:"order"`

	// Columns is the baseline: the SHA-256 of every value joka last applied,
	// keyed by column. It is the third point a merge needs — with the
	// declaration and the live row it says which side moved, where two points
	// can only say that they differ.
	//
	// Nil for a row written before the baseline existed, or by a reimport that
	// could not resolve one. A nil baseline reads as "unknown", which behaves
	// exactly as joka did before it: every difference is a conflict.
	Columns map[string]string `json:"columns,omitempty"`
}

// Baseline returns the recorded hash for one column and whether there is one.
func (e EntityState) Baseline(column string) (string, bool) {
	hash, ok := e.Columns[column]
	return hash, ok
}

// NewState returns an empty state at the current version.
func NewState() *State {
	return &State{
		Version:  StateVersion,
		Files:    make(map[string]FileState),
		Entities: make(map[string]EntityState),
	}
}

// Row returns the state of one _id, and whether joka has written it.
func (s *State) Row(refID string) (EntityState, bool) {
	e, ok := s.Entities[refID]
	return e, ok
}

// Track records the row an _id became, replacing any earlier record of it.
func (s *State) Track(refID string, e EntityState) {
	if s.Entities == nil {
		s.Entities = make(map[string]EntityState)
	}
	s.Entities[refID] = e
}

// Forget drops the record of an _id without touching the row it points at.
func (s *State) Forget(refID string) {
	delete(s.Entities, refID)
}

// TrackFile records a file's content hash as applied.
func (s *State) TrackFile(path, contentHash string) {
	if s.Files == nil {
		s.Files = make(map[string]FileState)
	}
	s.Files[path] = FileState{ContentHash: contentHash}
}

// ForgetFile drops a file's record and every row tracked against it, rows with
// no _id included. The rows in the database are untouched — this is what
// `entity forget` does, and the whole point of it is that the data stays.
func (s *State) ForgetFile(path string) {
	delete(s.Files, path)

	for refID, e := range s.Entities {
		if e.File == path {
			delete(s.Entities, refID)
		}
	}

	kept := s.Unkeyed[:0]
	for _, row := range s.Unkeyed {
		if row.EntityFile != path {
			kept = append(kept, row)
		}
	}
	s.Unkeyed = kept
}

// FileHash returns a file's recorded content hash and whether joka has synced
// it at all. An empty hash with tracked true is a file synced before hashing
// existed; app.FileStatusFor is what turns the pair into a verdict.
func (s *State) FileHash(path string) (hash string, tracked bool) {
	f, ok := s.Files[path]
	return f.ContentHash, ok
}

// RowsInFile returns the rows last declared in one file, in the order they were
// written. Used where a file is still the unit of work — reimport's deletion
// order, forget's plan, the diff's alignment.
//
// Unkeyed rows for the file are included. They are still that file's rows: a
// forget has to drop them, a diff has to show them as tracked with no `_id`,
// and a count of what a file tracks that left them out would be wrong. Only
// matching on identity skips them, and matching reads Entities directly.
func (s *State) RowsInFile(path string) []TrackedRow {
	var out []TrackedRow

	for refID, e := range s.Entities {
		if e.File != path {
			continue
		}
		out = append(out, e.row(refID))
	}

	for _, row := range s.Unkeyed {
		if row.EntityFile == path {
			out = append(out, row)
		}
	}

	sortTrackedRows(out)
	return out
}

// AllRows returns every tracked row, unkeyed ones included, ordered by file and
// then by position so two reads of the same state produce the same slice.
func (s *State) AllRows() []TrackedRow {
	out := make([]TrackedRow, 0, len(s.Entities)+len(s.Unkeyed))

	for refID, e := range s.Entities {
		out = append(out, e.row(refID))
	}
	out = append(out, s.Unkeyed...)

	sortTrackedRows(out)
	return out
}

// row renders an entry as the flat row shape the file-scoped callers still use.
func (e EntityState) row(refID string) TrackedRow {
	return TrackedRow{
		EntityFile:     e.File,
		TableName:      e.Table,
		RowPK:          e.PKValue,
		PKColumn:       e.PKColumn,
		RefID:          refID,
		InsertionOrder: e.Order,
	}
}

// sortTrackedRows orders by file, then position, then _id. The last key is
// there because position is not unique within a file: only a dirty file's rows
// are re-numbered, so a row left behind by an edit can share a position with
// one that moved into it. Ordering has to be total or the output moves between
// runs on the same data.
func sortTrackedRows(rows []TrackedRow) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.EntityFile != b.EntityFile {
			return a.EntityFile < b.EntityFile
		}
		if a.InsertionOrder != b.InsertionOrder {
			return a.InsertionOrder < b.InsertionOrder
		}
		return a.RefID < b.RefID
	})
}

// Rekey moves the record held under one _id to another, leaving the row it
// names untouched. It reports whether there was anything to move.
//
// This is what a declared `moved:` entry does. Renaming an _id whose natural
// key stays put needs no declaration — adoption finds the row again and sync
// infers the move — but a rename that also changes the unique key is
// indistinguishable from a delete plus an insert, and only the author can say
// which it was.
//
// It refuses to overwrite: two _ids collapsing into one would silently drop a
// row's tracking, and false says so rather than doing it.
func (s *State) Rekey(from, to string) bool {
	e, tracked := s.Entities[from]
	if !tracked {
		return false
	}
	if _, taken := s.Entities[to]; taken {
		return false
	}

	delete(s.Entities, from)
	s.Entities[to] = e
	return true
}
