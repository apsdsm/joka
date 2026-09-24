package app

import (
	"context"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// StateBackend is where a database's state document lives.
//
// It is an interface rather than a concrete type because the location is a
// deployment decision: the document can sit in the database it describes, in a
// file, or in an object store. Only the database backend can write the document
// in the same transaction as the rows it describes, which is why it is the
// default and the only one implemented; see
// proposal_entity_convergence_20260918.md.
//
// Load and Save move the whole document. That is the access pattern every
// caller already has — a reconcile reads all the tracking up front and writes
// it back at the end — and it is what lets one serialization serve every
// backend.
type StateBackend interface {
	// Load reads the state of the database. A database joka has never written
	// loads as an empty state, not an error.
	Load(ctx context.Context) (*domain.State, error)

	// Save writes the document back. The caller is expected to hold the
	// advisory lock, and for the database backend to be inside the transaction
	// that wrote the rows the document describes.
	Save(ctx context.Context, state *domain.State) error
}
