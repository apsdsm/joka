package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/apsdsm/joka/cmd/shared"
	"github.com/apsdsm/joka/internal/domains/migration/app"
	"github.com/apsdsm/joka/internal/domains/migration/domain"
	"github.com/apsdsm/joka/internal/domains/migration/infra"
	"github.com/fatih/color"
)

type RunMigrateStatusCommand struct {
	DB            *sql.DB
	MigrationsDir string
	OutputFormat  string
}

func (r RunMigrateStatusCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON

	if !jsonOut {
		color.Green("Checking migration chain...")
	}

	chain, err := app.GetMigrationChainAction{
		DB:            infra.NewPostgresDBAdapter(r.DB),
		MigrationsDir: r.MigrationsDir,
	}.Execute(ctx)

	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		if errors.Is(err, domain.ErrNoMigrationTable) {
			color.Red("Migrations table does not exist.")
		} else {
			color.Red("Error checking migration status: %v", err)
		}
		return err
	}

	if jsonOut {
		type migrationEntry struct {
			Index  string `json:"index"`
			Status string `json:"status"`
		}
		entries := make([]migrationEntry, len(chain))
		for i, m := range chain {
			entries[i] = migrationEntry{Index: m.MigrationIndex, Status: string(m.Status)}
		}
		shared.PrintJSON(map[string]any{"status": "ok", "migrations": entries})
		return nil
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
