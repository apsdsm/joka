package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/fatih/color"
	"github.com/nickfiggins/joka/internal/domains/migration/app"
	"github.com/nickfiggins/joka/internal/domains/migration/domain"
	"github.com/nickfiggins/joka/internal/domains/migration/infra"
)

type RunMigrateStatusCommand struct {
	DB            *sql.DB
	MigrationsDir string
}

func (r RunMigrateStatusCommand) Execute(ctx context.Context) error {
	color.Green("Checking migration chain...")

	// get migration chain
	chain, err := app.GetMigrationChainAction{
		DB:            infra.NewMySQLDBAdapter(r.DB),
		MigrationsDir: r.MigrationsDir,
	}.Execute(ctx)

	if errors.Is(err, domain.ErrNoMigrationTable) {
		color.Red("Migrations table does not exist.")
		return err
	} else if err != nil {
		color.Red("Error checking migration status: %v", err)
		return err
	}

	// if no migrations found, return nil
	if len(chain) == 0 {
		fmt.Println("No migration files found.")
		return nil
	}

	// print all migrations then return nil
	for _, m := range chain {
		fmt.Printf("Migration %s - Status: %s\n", m.MigrationIndex, m.Status)
	}

	return nil
}
