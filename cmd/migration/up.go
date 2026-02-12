package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/fatih/color"
	"github.com/apsdsm/joka/cmd/shared"
	lockinfra "github.com/apsdsm/joka/internal/domains/lock/infra"
	"github.com/apsdsm/joka/internal/domains/migration/app"
	"github.com/apsdsm/joka/internal/domains/migration/domain"
	"github.com/apsdsm/joka/internal/domains/migration/infra"
)

// RunMigrateUpCommand handles the "migrate up" command. It builds the migration
// chain, identifies pending migrations, and applies them inside a transaction.
type RunMigrateUpCommand struct {
	DB            *sql.DB
	MigrationsDir string
	AutoConfirm   bool
}

// Execute acquires an advisory lock, applies all pending migrations in a
// single transaction, and releases the lock when done (including on error).
func (r RunMigrateUpCommand) Execute(ctx context.Context) error {
	// Acquire advisory lock to prevent concurrent migration runs.
	lockAdapter := lockinfra.NewMySQLLockAdapter(r.DB)
	if err := lockAdapter.Acquire(ctx, "migrate up"); err != nil {
		return err
	}
	defer lockAdapter.Release(ctx)

	color.Green("Checking migration chain...")

	adapter := infra.NewMySQLDBAdapter(r.DB)
	chain, err := app.GetMigrationChainAction{
		DB:            adapter,
		MigrationsDir: r.MigrationsDir,
	}.Execute(ctx)

	if err != nil {
		if errors.Is(err, domain.ErrNoMigrationTable) {
			color.Red("Migrations table does not exist.")
			return err
		}

		color.Red("Error applying migrations: %v", err)
		return err
	}

	for _, m := range chain {
		fmt.Printf("Migration %s - Status: %s\n", m.MigrationIndex, m.Status)
	}

	var pending []domain.Migration
	for _, m := range chain {
		if m.Status == domain.StatusPending {
			pending = append(pending, m)
		}
	}

	if len(pending) == 0 {
		fmt.Println("No pending migrations to apply.")
		return nil
	}

	if !r.AutoConfirm {
		if !shared.Confirm(fmt.Sprintf("%d pending migrations found. Apply now? (only 'yes' will apply): ", len(pending))) {
			fmt.Println("Migration aborted by user.")
			return nil
		}
	}

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}

	txAdapter := infra.NewMySQLTxDBAdapter(tx, r.DB)

	for _, m := range pending {
		fmt.Printf("Applying migration %s...\n", m.MigrationIndex)
		err = app.ApplyAction{
			DB:        txAdapter,
			Migration: m,
		}.Execute(ctx)

		if err != nil {
			tx.Rollback()
			color.Red("Error applying migrations: %v", err)
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	color.Green("All migrations applied successfully.")
	return nil
}
