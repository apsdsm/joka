package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/fatih/color"
	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/cmd/shared"
	lockinfra "github.com/apsdsm/joka/internal/domains/lock/infra"
	"github.com/apsdsm/joka/internal/domains/migration/app"
	"github.com/apsdsm/joka/internal/domains/migration/domain"
)

// RunMigrateUpCommand handles the "migrate up" command. It builds the migration
// chain, identifies pending migrations, and applies them inside a transaction.
type RunMigrateUpCommand struct {
	DB            *sql.DB
	Driver        jokadb.Driver
	MigrationsDir string
	AutoConfirm   bool
	OutputFormat  string
}

// Execute acquires an advisory lock, applies all pending migrations in a
// single transaction, and releases the lock when done (including on error).
func (r RunMigrateUpCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON

	// Acquire advisory lock to prevent concurrent migration runs.
	lockAdapter := lockinfra.NewLockAdapter(r.Driver, r.DB)
	if err := lockAdapter.Acquire(ctx, "migrate up"); err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}
	defer lockAdapter.Release(ctx)

	if !jsonOut {
		color.Green("Checking migration chain...")
	}

	adapter := newMigrationAdapter(r.Driver, r.DB)
	chain, err := app.GetMigrationChainAction{
		DB:            adapter,
		MigrationsDir: r.MigrationsDir,
	}.Execute(ctx)

	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		if errors.Is(err, domain.ErrNoMigrationTable) {
			color.Red("Migrations table does not exist.")
			return err
		}
		color.Red("Error applying migrations: %v", err)
		return err
	}

	if !jsonOut {
		for _, m := range chain {
			fmt.Printf("Migration %s - Status: %s\n", m.MigrationIndex, m.Status)
		}
	}

	var pending []domain.Migration
	for _, m := range chain {
		if m.Status == domain.StatusPending {
			pending = append(pending, m)
		}
	}

	if len(pending) == 0 {
		if jsonOut {
			shared.PrintJSON(map[string]any{"status": "ok", "applied": []string{}, "message": "no pending migrations"})
			return nil
		}
		fmt.Println("No pending migrations to apply.")
		return nil
	}

	if !r.AutoConfirm && !jsonOut {
		if !shared.Confirm(fmt.Sprintf("%d pending migrations found. Apply now? (only 'yes' will apply): ", len(pending))) {
			fmt.Println("Migration aborted by user.")
			return nil
		}
	}

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(fmt.Errorf("starting transaction: %w", err))
		}
		return fmt.Errorf("starting transaction: %w", err)
	}

	txAdapter := newMigrationTxAdapter(r.Driver, tx, r.DB)

	var applied []string
	for _, m := range pending {
		if !jsonOut {
			fmt.Printf("Applying migration %s...\n", m.MigrationIndex)
		}
		err = app.ApplyAction{
			DB:        txAdapter,
			Migration: m,
		}.Execute(ctx)

		if err != nil {
			tx.Rollback()
			if jsonOut {
				return shared.PrintErrorJSON(err)
			}
			color.Red("Error applying migrations: %v", err)
			return err
		}
		applied = append(applied, m.MigrationIndex)
	}

	if err := tx.Commit(); err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(fmt.Errorf("committing transaction: %w", err))
		}
		return fmt.Errorf("committing transaction: %w", err)
	}

	if jsonOut {
		shared.PrintJSON(map[string]any{"status": "ok", "applied": applied})
		return nil
	}

	color.Green("All migrations applied successfully.")
	return nil
}
