package migration

import (
	"context"
	"database/sql"
	"errors"

	"github.com/apsdsm/joka/cmd/shared"
	"github.com/apsdsm/joka/internal/domains/migration/app"
	"github.com/apsdsm/joka/internal/domains/migration/domain"
	"github.com/apsdsm/joka/internal/domains/migration/infra"
	"github.com/fatih/color"
)

// RunInitCommand handles the "init" command to create the migrations table.
type RunInitCommand struct {
	DB           *sql.DB
	OutputFormat string
}

// Execute creates the migrations tracking table in the database.
func (r RunInitCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON

	if !jsonOut {
		color.Green("Initializing migrations system...")
	}

	err := app.CreateMigrationTableAction{
		DB: infra.NewPostgresDBAdapter(r.DB),
	}.Execute(ctx)

	if errors.Is(err, domain.ErrMigrationAlreadyExists) {
		if jsonOut {
			shared.PrintJSON(map[string]string{"status": "ok", "message": "migrations table already exists"})
			return nil
		}
		color.Yellow("Migrations table already exists.")
		return nil
	} else if errors.Is(err, domain.ErrMigrationTableCreation) {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		color.Red("Error creating migrations table.")
		return err
	} else if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		color.Red("Unexpected error: %v", err)
		return err
	}

	if jsonOut {
		shared.PrintJSON(map[string]string{"status": "ok", "message": "migrations table created"})
		return nil
	}

	color.Green("Migrations table created successfully.")
	return nil
}
