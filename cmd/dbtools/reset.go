package dbtools

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/apsdsm/joka/cmd/entity"
	"github.com/apsdsm/joka/cmd/migration"
	"github.com/apsdsm/joka/cmd/shared"
	entityapp "github.com/apsdsm/joka/internal/domains/entity/app"
	lockinfra "github.com/apsdsm/joka/internal/domains/lock/infra"
	"github.com/fatih/color"
)

// RunResetCommand wipes every table in the database and re-runs the full seed
// pipeline (init -> migrate up -> entity sync). Destructive — confirms once for
// the whole flow.
type RunResetCommand struct {
	DB *sql.DB
	// Secrets resolves {{ asm.<source>.<key> }} entity template references
	// against the `secrets:` sources in .jokarc.yaml.
	Secrets       entityapp.SecretResolver
	MigrationsDir string
	EntitiesDir   string
	AutoConfirm   bool
	OutputFormat  string
	StateFile     string
	// Profile and JokaVersion are passed through to entity sync, which writes
	// the state file.
	Profile     string
	JokaVersion string
}

func (r RunResetCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON

	// Single outer lock covers the whole reset.
	lockAdapter := lockinfra.NewPostgresLockAdapter(r.DB)
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
		fmt.Println("  4. Sync entity data")
		fmt.Println()

		if !r.AutoConfirm {
			if !shared.Confirm("This is destructive. Type 'yes' to proceed: ") {
				color.Yellow("Reset cancelled.")
				return shared.ErrCancelled
			}
		}
	}

	// 1. Drop everything.
	if !jsonOut {
		color.Cyan("\n[1/4] Dropping all tables...")
	}
	if err := (RunDropCommand{
		DB:           r.DB,
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
		color.Cyan("\n[2/4] Initializing migrations table...")
	}
	if err := (migration.RunInitCommand{
		DB:           r.DB,
		OutputFormat: "text",
	}).Execute(ctx); err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(fmt.Errorf("init: %w", err))
		}
		return fmt.Errorf("init: %w", err)
	}

	// 3. Migrate up.
	if !jsonOut {
		color.Cyan("\n[3/4] Applying migrations...")
	}
	if err := (migration.RunMigrateUpCommand{
		DB:            r.DB,
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

	// 4. Entity sync.
	if !jsonOut {
		color.Cyan("\n[4/4] Syncing entities...")
	}
	if err := (entity.RunEntitySyncCommand{
		DB:          r.DB,
		Secrets:     r.Secrets,
		EntitiesDir: r.EntitiesDir,
		AutoConfirm: true,
		// Exempt from the delete gate. reset dropped every table a moment ago by
		// design, so there is nothing left for it to protect and a tracked row
		// nothing declares is debris from the database that used to be here.
		AllowDelete:  true,
		OutputFormat: "text",
		SkipLock:     true,
		StateFile:    r.StateFile,
		Profile:      r.Profile,
		JokaVersion:  r.JokaVersion,
		// A reset has just dropped and re-seeded everything, so every declared
		// entity is new and there is nothing for the database to have moved
		// out from under.
		OnConflict: entityapp.ConflictFile,
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
