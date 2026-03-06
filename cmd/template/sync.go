package template

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/fatih/color"
	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/cmd/shared"
	lockinfra "github.com/apsdsm/joka/internal/domains/lock/infra"
	"github.com/apsdsm/joka/internal/domains/template/app"
	"github.com/apsdsm/joka/internal/domains/template/domain"
	"github.com/apsdsm/joka/internal/domains/template/infra"
)

// RunDataSyncCommand handles the "data sync" command. It reads table configs
// and data files from the templates directory, then syncs them to the database.
type RunDataSyncCommand struct {
	DB                *sql.DB
	Driver            jokadb.Driver
	TemplatesDir      string
	Tables            []infra.TableConfig
	AutoConfirm       bool
	IgnoreForeignKeys bool
	OutputFormat      string
}

// Execute acquires an advisory lock, syncs all configured tables inside a
// transaction, and releases the lock when done.
func (r RunDataSyncCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON

	// Acquire advisory lock to prevent concurrent sync/migration runs.
	lockAdapter := lockinfra.NewLockAdapter(r.Driver, r.DB)
	if err := lockAdapter.Acquire(ctx, "data sync"); err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}
	defer lockAdapter.Release(ctx)

	tables, err := infra.GetTables(r.TemplatesDir, r.Tables)
	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		color.Red("Error: %v", err)
		return err
	}

	if len(tables) == 0 {
		if jsonOut {
			shared.PrintJSON(map[string]any{"status": "ok", "tables": []string{}, "message": "no tables configured"})
			return nil
		}
		color.Yellow("No tables configured for sync.")
		return nil
	}

	// Pre-load row counts for display/confirmation.
	type tablePreview struct {
		Table domain.Table
		Rows  int
		Files int
	}
	var previews []tablePreview
	for _, table := range tables {
		rows, err := app.LoadTableDataAction{Table: table}.Execute(ctx)
		if err != nil {
			if jsonOut {
				return shared.PrintErrorJSON(err)
			}
			return err
		}
		previews = append(previews, tablePreview{Table: table, Rows: len(rows), Files: len(table.Records)})
	}

	if !jsonOut {
		fmt.Println()
		color.Set(color.Bold)
		fmt.Println("Tables to sync:")
		color.Unset()
		for _, p := range previews {
			color.Cyan("  %s (%s): %d rows from %d files", p.Table.Name, p.Table.Strategy, p.Rows, p.Files)
		}
		fmt.Println()

		if !r.AutoConfirm {
			if !shared.Confirm("Proceed with sync? (only 'yes' will confirm): ") {
				color.Yellow("Sync cancelled.")
				return nil
			}
		}
	}

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(fmt.Errorf("starting transaction: %w", err))
		}
		return fmt.Errorf("starting transaction: %w", err)
	}

	txAdapter := newTemplateTxAdapter(r.Driver, tx, r.DB)

	if r.IgnoreForeignKeys {
		if err := txAdapter.DisableForeignKeys(ctx); err != nil {
			tx.Rollback()
			if jsonOut {
				return shared.PrintErrorJSON(fmt.Errorf("disabling foreign key checks: %w", err))
			}
			return fmt.Errorf("disabling foreign key checks: %w", err)
		}
	}

	type tableResult struct {
		Name       string `json:"name"`
		Strategy   string `json:"strategy"`
		RowsSynced int    `json:"rows_synced"`
	}
	var results []tableResult

	for _, table := range tables {
		if table.Strategy == domain.StrategyTruncate {
			if !jsonOut {
				color.Cyan("Syncing %s...", table.Name)
			}

			count, err := app.SyncTableAction{DB: txAdapter, Table: table}.Execute(ctx)
			if err != nil {
				tx.Rollback()
				if jsonOut {
					return shared.PrintErrorJSON(err)
				}
				return err
			}
			if !jsonOut {
				fmt.Printf("  Synced %d rows\n", count)
			}
			results = append(results, tableResult{Name: table.Name, Strategy: string(table.Strategy), RowsSynced: count})
		} else {
			if !jsonOut {
				color.Yellow("Strategy '%s' not yet implemented for %s, skipping.", table.Strategy, table.Name)
			}
			results = append(results, tableResult{Name: table.Name, Strategy: string(table.Strategy), RowsSynced: 0})
		}
	}

	if r.IgnoreForeignKeys {
		if err := txAdapter.EnableForeignKeys(ctx); err != nil {
			tx.Rollback()
			if jsonOut {
				return shared.PrintErrorJSON(fmt.Errorf("re-enabling foreign key checks: %w", err))
			}
			return fmt.Errorf("re-enabling foreign key checks: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(fmt.Errorf("committing transaction: %w", err))
		}
		return fmt.Errorf("committing transaction: %w", err)
	}

	if jsonOut {
		shared.PrintJSON(map[string]any{"status": "ok", "tables": results})
		return nil
	}

	fmt.Println()
	color.Green("Sync complete.")
	return nil
}

func newTemplateTxAdapter(driver jokadb.Driver, tx *sql.Tx, conn *sql.DB) app.DBAdapter {
	if driver == jokadb.Postgres {
		return infra.NewPostgresTxDBAdapter(tx, conn)
	}
	return infra.NewMySQLTxDBAdapter(tx, conn)
}
