package domain

import "errors"

var (
	ErrNoMigrationTable       = errors.New("migrations table does not exist")
	ErrMigrationAlreadyExists = errors.New("migrations table already exists")
	ErrMigrationTableCreation = errors.New("error creating migrations table")
)
