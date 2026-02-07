package migration

import (
	"context"
	"database/sql"
	"errors"

	"github.com/fatih/color"
	"github.com/nickfiggins/joka/internal/domains/migration/app"
	"github.com/nickfiggins/joka/internal/domains/migration/domain"
	"github.com/nickfiggins/joka/internal/domains/migration/infra"
)

// RunInitCommand handles the "init" command to create the migrations table.
type RunInitCommand struct {
	DB *sql.DB
}

// Execute creates the migrations tracking table in the database.
func (r RunInitCommand) Execute(ctx context.Context) error {
	color.Green("Initializing migrations system...")

	err := app.CreateMigrationTableAction{
		DB: infra.NewMySQLDBAdapter(r.DB),
	}.Execute(ctx)

	if errors.Is(err, domain.ErrMigrationAlreadyExists) {
		color.Yellow("Migrations table already exists.")
		return nil
	} else if errors.Is(err, domain.ErrMigrationTableCreation) {
		color.Red("Error creating migrations table.")
		return err
	} else if err != nil {
		color.Red("Unexpected error: %v", err)
		return err
	}

	color.Green("Migrations table created successfully.")
	return nil
}
