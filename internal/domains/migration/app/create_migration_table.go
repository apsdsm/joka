package app

import "context"

// CreateMigrationTableAction encapsulates the dependencies needed to create
// the migrations tracking table.
type CreateMigrationTableAction struct {
	DB DBAdapter
}

// Execute creates the migrations table in the database.
func (a CreateMigrationTableAction) Execute(ctx context.Context) error {
	return a.DB.CreateMigrationsTable(ctx)
}
