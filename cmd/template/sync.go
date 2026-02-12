package template

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/fatih/color"
	"github.com/apsdsm/joka/cmd/shared"
	lockinfra "github.com/apsdsm/joka/internal/domains/lock/infra"
	"github.com/apsdsm/joka/internal/domains/template/app"
	"github.com/apsdsm/joka/internal/domains/template/domain"
	"github.com/apsdsm/joka/internal/domains/template/infra"
)

// RunDataSyncCommand handles the "data sync" command. It reads table configs
// and data files from the templates directory, then syncs them to the database.
type RunDataSyncCommand struct {
	DB           *sql.DB
	TemplatesDir string
	AutoConfirm  bool
}

// Execute acquires an advisory lock, syncs all configured tables inside a
// transaction, and releases the lock when done.
func (r RunDataSyncCommand) Execute(ctx context.Context) error {
	// Acquire advisory lock to prevent concurrent sync/migration runs.
	lockAdapter := lockinfra.NewMySQLLockAdapter(r.DB)
	if err := lockAdapter.Acquire(ctx, "data sync"); err != nil {
		return err
	}
	defer lockAdapter.Release(ctx)

	tables, err := infra.GetTables(r.TemplatesDir)
	if err != nil {
		color.Red("Error: %v", err)
		return err
	}

	if len(tables) == 0 {
		color.Yellow("No tables configured for sync.")
		return nil
	}

	fmt.Println()
	color.Set(color.Bold)
	fmt.Println("Tables to sync:")
	color.Unset()
	for _, table := range tables {
		rows, err := app.LoadTableDataAction{Table: table}.Execute(ctx)
		if err != nil {
			return err
		}
		color.Cyan("  %s (%s): %d rows from %d files", table.Name, table.Strategy, len(rows), len(table.Records))
	}
	fmt.Println()

	if !r.AutoConfirm {
		if !shared.Confirm("Proceed with sync? (only 'yes' will confirm): ") {
			color.Yellow("Sync cancelled.")
			return nil
		}
	}

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}

	txAdapter := infra.NewMySQLTxDBAdapter(tx, r.DB)

	for _, table := range tables {
		if table.Strategy == domain.StrategyTruncate {
			color.Cyan("Syncing %s...", table.Name)

			count, err := app.SyncTableAction{DB: txAdapter, Table: table}.Execute(ctx)
			if err != nil {
				tx.Rollback()
				return err
			}
			fmt.Printf("  Synced %d rows\n", count)
		} else {
			color.Yellow("Strategy '%s' not yet implemented for %s, skipping.", table.Strategy, table.Name)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	fmt.Println()
	color.Green("Sync complete.")
	return nil
}
