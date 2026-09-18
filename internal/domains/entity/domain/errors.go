package domain

import "errors"

var (
	// ErrInvalidReference is returned when a template expression references
	// an _id that has not been defined by a parent entity.
	ErrInvalidReference = errors.New("invalid entity reference")

	// ErrInvalidTemplate is returned when a {{ ... }} expression cannot be
	// parsed or contains an unknown function.
	ErrInvalidTemplate = errors.New("invalid template expression")

	// ErrEntityParseFailed is returned when a YAML entity file cannot be
	// decoded into the expected structure.
	ErrEntityParseFailed = errors.New("entity parse failed")

	// ErrLookupNotFound is returned when a {{ lookup|... }} expression
	// matches zero rows in the target table.
	ErrLookupNotFound = errors.New("lookup returned no rows")

	// ErrDuplicateRefID is returned when two entities in the same file
	// share the same _id handle.
	ErrDuplicateRefID = errors.New("duplicate _id in entity file")

	// ErrEntityNotSynced is returned when reimport is requested for a file
	// that has never been synced.
	ErrEntityNotSynced = errors.New("entity file has not been synced")

	// ErrForeignKeyConflict is returned when a DELETE fails because another
	// row references it via a foreign key constraint.
	ErrForeignKeyConflict = errors.New("foreign key constraint prevented deletion")

	// ErrEntityMissingRefID is returned when entity update encounters an
	// entity without an _id handle, which is required to determine whether
	// it has already been tracked.
	ErrEntityMissingRefID = errors.New("all entities must have _id for entity update")

	// ErrRowsStillLive is returned when entity forget is asked to drop the
	// tracking for a file whose rows are still in the database. Forgetting
	// them would leave rows nothing tracks, and the next sync would insert a
	// second copy, so the caller must pass --force to mean it.
	ErrRowsStillLive = errors.New("tracked rows are still in the database")

	// ErrEntitySetInvalid means the entity set breaks an invariant the _id
	// identity model needs: every entity declares an _id, and no _id is claimed
	// twice. Callers needing to know which invariant broke inspect the
	// []EntitySetProblem the validator returns, which carries the file and
	// position of every entity involved.
	ErrEntitySetInvalid = errors.New("entity set is not valid")

	// ErrEntityTableChanged means an _id is tracked as a row in one table but is
	// now declared in another. An _id names one row; the new declaration is a
	// different thing wearing the same name.
	ErrEntityTableChanged = errors.New("_id now declares a different table")

	// ErrStateAmbiguous means the tracking in the database cannot be read as a
	// state document because one _id is claimed by more than one tracked row.
	// Tracking version 2's unique index makes this unreachable going forward;
	// it is reachable on a database whose upgrade to version 2 is still blocked
	// on exactly this, which is where it gets resolved.
	ErrStateAmbiguous = errors.New("tracking cannot be read: an _id is claimed by more than one row")
)
