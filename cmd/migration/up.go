package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/apsdsm/joka/cmd/shared"
	lockinfra "github.com/apsdsm/joka/internal/domains/lock/infra"
	"github.com/apsdsm/joka/internal/domains/migration/app"
	"github.com/apsdsm/joka/internal/domains/migration/domain"
	"github.com/apsdsm/joka/internal/domains/migration/infra"
	"github.com/fatih/color"
)

// RunMigrateUpCommand handles the "migrate up" command. It builds the migration
// chain, identifies pending migrations, and applies them inside a transaction.
type RunMigrateUpCommand struct {
	DB            *sql.DB
	MigrationsDir string
	AutoConfirm   bool
	OutputFormat  string
	// SkipLock skips advisory lock acquisition. Used when an outer command
	// (e.g. `joka reset`) already holds the lock.
	SkipLock bool
	// Output is where progress is written. Nil means os.Stdout, which is what
	// the CLI wants; the library facade passes io.Discard, because a package a
	// test helper calls has no business writing to the process stdout.
	Output io.Writer
}

func (r RunMigrateUpCommand) out() io.Writer {
	if r.Output != nil {
		return r.Output
	}
	return os.Stdout
}

// Execute acquires an advisory lock, applies all pending migrations in a
// single transaction, and releases the lock when done (including on error).
func (r RunMigrateUpCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON

	if !r.SkipLock {
		// Acquire advisory lock to prevent concurrent migration runs.
		lockAdapter := lockinfra.NewPostgresLockAdapter(r.DB)
		if err := lockAdapter.Acquire(ctx, "migrate up"); err != nil {
			return err
		}
		defer lockAdapter.Release(ctx)
	}

	if !jsonOut {
		color.New(color.FgGreen).Fprintln(r.out(), "Checking migration chain...")
	}

	adapter := infra.NewPostgresDBAdapter(r.DB)
	chain, err := app.GetMigrationChainAction{
		DB:            adapter,
		MigrationsDir: r.MigrationsDir,
	}.Execute(ctx)

	if err != nil {
		// Returned, not printed. main renders whatever a command returns, so a
		// command that also prints its error says it twice — which is what
		// `migrate verify` on a database with no snapshot was doing, and every
		// other command with it. Anything worth adding is added to the error.
		if errors.Is(err, domain.ErrNoMigrationTable) {
			return fmt.Errorf("%w: run 'joka init' first", err)
		}

		return fmt.Errorf("applying migrations: %w", err)
	}

	if !jsonOut {
		for _, m := range chain {
			fmt.Fprintf(r.out(), "Migration %s - Status: %s\n", m.MigrationIndex, m.Status)
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
		fmt.Fprintln(r.out(), "No pending migrations to apply.")
		return nil
	}

	if !r.AutoConfirm && !jsonOut {
		if !shared.Confirm(fmt.Sprintf("%d pending migrations found. Apply now? (only 'yes' will apply): ", len(pending))) {
			fmt.Fprintln(r.out(), "Migration aborted by user.")
			return shared.ErrCancelled
		}
	}

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}

	// Fail fast on lock contention rather than hanging indefinitely: a DDL
	// migration that cannot acquire its lock, because an app is still holding
	// the table, errors out in seconds instead of wedging.
	if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout = '15s'"); err != nil {
		tx.Rollback() //nolint:errcheck
		return fmt.Errorf("setting lock_timeout: %w", err)
	}

	txAdapter := infra.NewPostgresTxDBAdapter(tx, r.DB)

	var applied []string
	for _, m := range pending {
		if !jsonOut {
			fmt.Fprintf(r.out(), "Applying migration %s...\n", m.MigrationIndex)
		}
		err = app.ApplyAction{
			DB:        txAdapter,
			Migration: m,
		}.Execute(ctx)

		if err != nil {
			tx.Rollback() //nolint:errcheck
			return fmt.Errorf("applying %s: %w", m.MigrationIndex, err)
		}
		applied = append(applied, m.MigrationIndex)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	if jsonOut {
		shared.PrintJSON(map[string]any{"status": "ok", "applied": applied})
		return nil
	}

	color.New(color.FgGreen).Fprintln(r.out(), "All migrations applied successfully.")
	return nil
}
