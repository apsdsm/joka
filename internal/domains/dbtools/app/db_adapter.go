package app

import "context"

// DBAdapter abstracts DB-wide operations used by the dbtools domain.
type DBAdapter interface {
	// ListTables returns all user tables in the current database/schema,
	// including joka_* tracking tables.
	ListTables(ctx context.Context) ([]string, error)

	// DropAllTables drops every table returned by ListTables. Foreign key
	// constraints are bypassed (MySQL: disable FK checks; Postgres: CASCADE).
	DropAllTables(ctx context.Context) error
}
