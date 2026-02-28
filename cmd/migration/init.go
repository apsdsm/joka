package migration

import (
	"context"
	"database/sql"
	"errors"

	"github.com/fatih/color"
	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/domains/migration/app"
	"github.com/apsdsm/joka/internal/domains/migration/domain"
	"github.com/apsdsm/joka/internal/domains/migration/infra"
)

// RunInitCommand handles the "init" command to create the migrations table.
type RunInitCommand struct {
	DB     *sql.DB
	Driver jokadb.Driver
}

// Execute creates the migrations tracking table in the database.
func (r RunInitCommand) Execute(ctx context.Context) error {
	color.Green("Initializing migrations system...")

	err := app.CreateMigrationTableAction{
		DB: newMigrationAdapter(r.Driver, r.DB),
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

func newMigrationAdapter(driver jokadb.Driver, conn *sql.DB) app.DBAdapter {
	if driver == jokadb.Postgres {
		return infra.NewPostgresDBAdapter(conn)
	}
	return infra.NewMySQLDBAdapter(conn)
}

func newMigrationTxAdapter(driver jokadb.Driver, tx *sql.Tx, conn *sql.DB) app.DBAdapter {
	if driver == jokadb.Postgres {
		return infra.NewPostgresTxDBAdapter(tx, conn)
	}
	return infra.NewMySQLTxDBAdapter(tx, conn)
}
