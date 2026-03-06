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
)
