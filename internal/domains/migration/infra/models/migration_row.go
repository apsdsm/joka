package models

import "time"

// MigrationRow represents a row in the migrations table, mapping directly
// to the columns stored in the database.
type MigrationRow struct {
	ID             int       `db:"id"`
	MigrationIndex string    `db:"migration_index"`
	AppliedAt      time.Time `db:"applied_at"`
}
