package domain

import "errors"

var (
	ErrNoMigrationTable       = errors.New("migrations table does not exist")
	ErrMigrationAlreadyExists = errors.New("migrations table already exists")
	ErrMigrationTableCreation = errors.New("error creating migrations table")

	// ErrSchemaNotApplicable means generated schema SQL failed to apply to a
	// scratch schema — it would not rebuild the database it came from.
	ErrSchemaNotApplicable = errors.New("generated schema is not applicable")

	// ErrSchemaValidationUnsupported means the driver cannot dry-run schema SQL
	// (MySQL: DDL is not transactional). The SQL is unverified, not invalid.
	ErrSchemaValidationUnsupported = errors.New("schema validation is not supported on this driver")

	// ErrUnsupportedSchemaObjects means the schema contains objects the snapshot
	// cannot represent, so a consolidated baseline would silently drop them.
	ErrUnsupportedSchemaObjects = errors.New("schema contains objects that cannot be consolidated")
)
