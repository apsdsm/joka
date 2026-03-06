package entity

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/fatih/color"
	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/cmd/shared"
	"github.com/apsdsm/joka/internal/domains/entity/app"
	"github.com/apsdsm/joka/internal/domains/entity/domain"
	"github.com/apsdsm/joka/internal/domains/entity/infra"
	lockinfra "github.com/apsdsm/joka/internal/domains/lock/infra"
)

// RunEntitySyncCommand handles the "entity sync" command.
type RunEntitySyncCommand struct {
	DB           *sql.DB
	Driver       jokadb.Driver
	EntitiesDir  string
	AutoConfirm  bool
	OutputFormat string
}

func (r RunEntitySyncCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON

	lockAdapter := lockinfra.NewLockAdapter(r.Driver, r.DB)

	if err := lockAdapter.Acquire(ctx, "entity sync"); err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	defer lockAdapter.Release(ctx) //nolint:errcheck

	dbAdapter := newEntityAdapter(r.Driver, r.DB)

	if err := dbAdapter.EnsureTrackingTable(ctx); err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(fmt.Errorf("ensuring tracking table: %w", err))
		}
		return fmt.Errorf("ensuring tracking table: %w", err)
	}

	if err := dbAdapter.EnsureRowTrackingTable(ctx); err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(fmt.Errorf("ensuring row tracking table: %w", err))
		}
		return fmt.Errorf("ensuring row tracking table: %w", err)
	}

	if err := dbAdapter.EnsureContentHashColumn(ctx); err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(fmt.Errorf("ensuring content hash column: %w", err))
		}
		return fmt.Errorf("ensuring content hash column: %w", err)
	}

	relPaths, err := infra.DiscoverEntityFiles(r.EntitiesDir)
	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	if len(relPaths) == 0 {
		if jsonOut {
			shared.PrintJSON(map[string]any{"status": "ok", "synced": []string{}, "message": "no entity files found"})
			return nil
		}
		color.Yellow("No entity files found in %s.", r.EntitiesDir)
		return nil
	}

	var pending []*domain.EntityFile

	for _, rel := range relPaths {
		fullPath := filepath.Join(r.EntitiesDir, rel)

		already, err := dbAdapter.IsEntitySynced(ctx, rel)
		if err != nil {
			if jsonOut {
				return shared.PrintErrorJSON(err)
			}
			return err
		}

		if already {
			continue
		}

		file, err := app.ParseEntityAction{Path: fullPath}.Execute()
		if err != nil {
			if jsonOut {
				return shared.PrintErrorJSON(err)
			}
			return err
		}

		file.Path = rel

		hash, err := app.HashFileContent(fullPath)
		if err != nil {
			if jsonOut {
				return shared.PrintErrorJSON(err)
			}
			return err
		}
		file.ContentHash = hash

		pending = append(pending, file)
	}

	if len(pending) == 0 {
		if jsonOut {
			shared.PrintJSON(map[string]any{"status": "ok", "synced": []string{}, "message": "all entity files already synced"})
			return nil
		}
		color.Green("All entity files already synced.")
		return nil
	}

	if !jsonOut {
		fmt.Println()
		color.Set(color.Bold)
		fmt.Println("Entity files to sync:")
		color.Unset()

		for _, file := range pending {
			color.Cyan("  %s (%d entities)", file.Path, len(file.Entities))
		}

		fmt.Println()

		if !r.AutoConfirm {
			if !shared.Confirm("Proceed with entity sync? (only 'yes' will confirm): ") {
				color.Yellow("Entity sync cancelled.")
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

	txAdapter := newEntityTxAdapter(r.Driver, tx, r.DB)

	synced, err := app.SyncEntitiesAction{
		DB:    txAdapter,
		Files: pending,
	}.Execute(ctx)
	if err != nil {
		tx.Rollback() //nolint:errcheck
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	if err := tx.Commit(); err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(fmt.Errorf("committing transaction: %w", err))
		}
		return fmt.Errorf("committing transaction: %w", err)
	}

	if jsonOut {
		shared.PrintJSON(map[string]any{"status": "ok", "synced": synced})
		return nil
	}

	fmt.Println()

	for _, path := range synced {
		color.Green("  Synced: %s", path)
	}

	color.Green("\nEntity sync complete. %d file(s) synced.", len(synced))

	return nil
}

func newEntityAdapter(driver jokadb.Driver, conn *sql.DB) app.DBAdapter {
	if driver == jokadb.Postgres {
		return infra.NewPostgresDBAdapter(conn)
	}
	return infra.NewMySQLDBAdapter(conn)
}

func newEntityTxAdapter(driver jokadb.Driver, tx *sql.Tx, conn *sql.DB) app.DBAdapter {
	if driver == jokadb.Postgres {
		return infra.NewPostgresTxDBAdapter(tx, conn)
	}
	return infra.NewMySQLTxDBAdapter(tx, conn)
}
