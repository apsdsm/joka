package domain

import "errors"

var (
	ErrNoMigrationTable       = errors.New("migrations table does not exist")
	ErrMigrationAlreadyExists = errors.New("migrations table already exists")
	ErrMigrationTableCreation = errors.New("error creating migrations table")

	// ErrDumpToolMissing means the database's own dump binary (pg_dump /
	// mysqldump) could not be found on PATH.
	ErrDumpToolMissing = errors.New("dump tool not found")

	// ErrDumpNotApplicable means the dump output could not be turned into SQL
	// joka is able to apply, so it must not be written as a migration.
	ErrDumpNotApplicable = errors.New("dump output is not applicable by joka")

	// ErrDumpDriverUnsupported means joka cannot build a consolidated baseline
	// for this driver yet. PostgreSQL only for now; MySQL needs SQL-splitter work
	// before mysqldump output can be applied.
	ErrDumpDriverUnsupported = errors.New("consolidation is not supported on this driver yet")

	// ErrNotLastApplied means consolidation was asked to target a migration that
	// is not the most recently applied one. A dump describes the schema now, so
	// only the latest applied migration can carry the baseline.
	ErrNotLastApplied = errors.New("target is not the last applied migration")
)
