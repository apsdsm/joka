package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"

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
	// Output is where progress is written. Nil means os.Stdout — see
	// RunMigrateUpCommand.Output.
	Output io.Writer
}

func (r RunInitCommand) out() io.Writer {
	if r.Output != nil {
		return r.Output
	}
	return os.Stdout
}

// Execute creates the migrations tracking table in the database.
func (r RunInitCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON

	if !jsonOut {
		color.New(color.FgGreen).Fprintln(r.out(), "Initializing migrations system...")
	}

	err := app.CreateMigrationTableAction{
		DB: infra.NewPostgresDBAdapter(r.DB),
	}.Execute(ctx)

	if errors.Is(err, domain.ErrMigrationAlreadyExists) {
		if jsonOut {
			shared.PrintJSON(map[string]string{"status": "ok", "message": "migrations table already exists"})
			return nil
		}
		color.New(color.FgYellow).Fprintln(r.out(), "Migrations table already exists.")
		return nil
	} else if errors.Is(err, domain.ErrMigrationTableCreation) {
		return fmt.Errorf("creating the migrations table: %w", err)
	} else if err != nil {
		return err
	}

	if jsonOut {
		shared.PrintJSON(map[string]string{"status": "ok", "message": "migrations table created"})
		return nil
	}

	color.New(color.FgGreen).Fprintln(r.out(), "Migrations table created successfully.")
	return nil
}
