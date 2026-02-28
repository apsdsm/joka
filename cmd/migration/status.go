package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/fatih/color"
	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/domains/migration/app"
	"github.com/apsdsm/joka/internal/domains/migration/domain"
)

type RunMigrateStatusCommand struct {
	DB            *sql.DB
	Driver        jokadb.Driver
	MigrationsDir string
}

func (r RunMigrateStatusCommand) Execute(ctx context.Context) error {
	color.Green("Checking migration chain...")

	chain, err := app.GetMigrationChainAction{
		DB:            newMigrationAdapter(r.Driver, r.DB),
		MigrationsDir: r.MigrationsDir,
	}.Execute(ctx)

	if errors.Is(err, domain.ErrNoMigrationTable) {
		color.Red("Migrations table does not exist.")
		return err
	} else if err != nil {
		color.Red("Error checking migration status: %v", err)
		return err
	}

	if len(chain) == 0 {
		fmt.Println("No migration files found.")
		return nil
	}

	for _, m := range chain {
		fmt.Printf("Migration %s - Status: %s\n", m.MigrationIndex, m.Status)
	}

	return nil
}
