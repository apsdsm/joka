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

	// ErrEntityNotSynced is returned when an operation is requested for a file
	// that has never been synced.
	ErrEntityNotSynced = errors.New("entity file has not been synced")

	// ErrForeignKeyConflict is returned when a DELETE fails because another
	// row references it via a foreign key constraint.
	//
	// Sync deletes a tracked entity no file declares any more, children before
	// parents, but a foreign key from outside the seeded set is something it
	// cannot order around. The run rolls back and names the row.
	ErrForeignKeyConflict = errors.New("foreign key constraint prevented deletion")

	// ErrRowsStillLive is returned when entity forget is asked to drop the
	// tracking for a file whose rows are still in the database. Forgetting
	// them hands live rows to nobody, so the caller must pass --force to mean
	// it.
	//
	// It used to say the next sync would insert a second copy. Adoption made
	// that false: a file that still declares the entity finds the row by its
	// unique key and claims it back. The refusal stands because giving up
	// ownership of live rows is worth confirming, not because it duplicates.
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

	// ErrRowNotFound means a row joka tracks is no longer in the database.
	// Somebody deleted it outside joka, or a restore lost it. It is not a
	// failure to read: the row is gone, which is a fact about the database, and
	// a convergence tool answers it by putting the row back.
	ErrRowNotFound = errors.New("tracked row is not in the database")

	// ErrEntityConflict means the database moved out from under the
	// declaration: a column joka wrote holds a value joka did not write, so
	// applying the file would discard a change it cannot account for. Which
	// side wins is --on-conflict; refusing is the default, because a sync that
	// silently overwrote it would be the failure this whole model exists to
	// prevent.
	ErrEntityConflict = errors.New("the database changed since joka last wrote")

	// ErrStateAmbiguous means the tracking in the database cannot be read as a
	// state document because one _id is claimed by more than one tracked row.
	// Tracking version 2's unique index makes this unreachable going forward;
	// it is reachable on a database whose upgrade to version 2 is still blocked
	// on exactly this, which is where it gets resolved.
	ErrStateAmbiguous = errors.New("tracking cannot be read: an _id is claimed by more than one row")

	// ErrWrongDatabase means the state file beside the working directory
	// describes a database this is not, so a command that writes refuses.
	//
	// It is a refusal rather than a warning because of adoption: an untracked
	// entity is looked up by its unique key and claimed, so a run against the
	// wrong database no longer fails loudly on a duplicate key. It takes over
	// the rows it finds and writes the declaration over them. The identity
	// marker is what stands between that and a database nobody meant to touch.
	ErrWrongDatabase = errors.New("the state file describes a different database")
)
