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
)
