package dbtools

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/apsdsm/joka/cmd/entity"
	"github.com/apsdsm/joka/cmd/migration"
	"github.com/apsdsm/joka/cmd/shared"
	"github.com/apsdsm/joka/cmd/template"
	jokadb "github.com/apsdsm/joka/db"
	lockinfra "github.com/apsdsm/joka/internal/domains/lock/infra"
	templateinfra "github.com/apsdsm/joka/internal/domains/template/infra"
	"github.com/fatih/color"
)

// RunResetCommand wipes every table in the database and re-runs the full seed
// pipeline (init -> migrate up -> data sync -> entity sync). Destructive —
// confirms once for the whole flow.
type RunResetCommand struct {
	DB                *sql.DB
	Driver            jokadb.Driver
	MigrationsDir     string
	TemplatesDir      string
	EntitiesDir       string
	Tables            []templateinfra.TableConfig
	IgnoreForeignKeys bool
	AutoConfirm       bool
	OutputFormat      string
}

func (r RunResetCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON

	// Single outer lock covers the whole reset.
	lockAdapter := lockinfra.NewLockAdapter(r.Driver, r.DB)
	if err := lockAdapter.Acquire(ctx, "reset"); err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}
	defer lockAdapter.Release(ctx) //nolint:errcheck

	if !jsonOut {
		fmt.Println()
		color.Red("joka reset will:")
		fmt.Println("  1. Drop every table in the current database (including joka_* tracking)")
		fmt.Println("  2. Re-create the migrations table (init)")
		fmt.Println("  3. Apply all migrations from scratch")
		fmt.Println("  4. Sync template data")
		fmt.Println("  5. Sync entity data")
		fmt.Println()

		if !r.AutoConfirm {
			if !shared.Confirm("This is destructive. Type 'yes' to proceed: ") {
				color.Yellow("Reset cancelled.")
				return nil
			}
		}
	}

	// 1. Drop everything.
	if !jsonOut {
		color.Cyan("\n[1/5] Dropping all tables...")
	}
	if err := (RunDropCommand{
		DB:           r.DB,
		Driver:       r.Driver,
		AutoConfirm:  true,
		OutputFormat: "text",
		SkipLock:     true,
	}).Execute(ctx); err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(fmt.Errorf("drop: %w", err))
		}
		return fmt.Errorf("drop: %w", err)
	}

	// 2. Init migrations table.
	if !jsonOut {
		color.Cyan("\n[2/5] Initializing migrations table...")
	}
	if err := (migration.RunInitCommand{
		DB:           r.DB,
		Driver:       r.Driver,
		OutputFormat: "text",
	}).Execute(ctx); err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(fmt.Errorf("init: %w", err))
		}
		return fmt.Errorf("init: %w", err)
	}

	// 3. Migrate up.
	if !jsonOut {
		color.Cyan("\n[3/5] Applying migrations...")
	}
	if err := (migration.RunMigrateUpCommand{
		DB:            r.DB,
		Driver:        r.Driver,
		MigrationsDir: r.MigrationsDir,
		AutoConfirm:   true,
		OutputFormat:  "text",
		SkipLock:      true,
	}).Execute(ctx); err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(fmt.Errorf("migrate up: %w", err))
		}
		return fmt.Errorf("migrate up: %w", err)
	}

	// 4. Data sync.
	if !jsonOut {
		color.Cyan("\n[4/5] Syncing template data...")
	}
	if err := (template.RunDataSyncCommand{
		DB:                r.DB,
		Driver:            r.Driver,
		TemplatesDir:      r.TemplatesDir,
		Tables:            r.Tables,
		AutoConfirm:       true,
		IgnoreForeignKeys: r.IgnoreForeignKeys,
		OutputFormat:      "text",
		SkipLock:          true,
	}).Execute(ctx); err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(fmt.Errorf("data sync: %w", err))
		}
		return fmt.Errorf("data sync: %w", err)
	}

	// 5. Entity sync.
	if !jsonOut {
		color.Cyan("\n[5/5] Syncing entities...")
	}
	if err := (entity.RunEntitySyncCommand{
		DB:           r.DB,
		Driver:       r.Driver,
		EntitiesDir:  r.EntitiesDir,
		AutoConfirm:  true,
		OutputFormat: "text",
		SkipLock:     true,
	}).Execute(ctx); err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(fmt.Errorf("entity sync: %w", err))
		}
		return fmt.Errorf("entity sync: %w", err)
	}

	if jsonOut {
		shared.PrintJSON(map[string]any{"status": "ok", "message": "reset complete"})
		return nil
	}

	fmt.Println()
	color.Green("Reset complete.")
	return nil
}
