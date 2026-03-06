package entity

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fatih/color"
	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/cmd/shared"
	"github.com/apsdsm/joka/internal/domains/entity/app"
	lockinfra "github.com/apsdsm/joka/internal/domains/lock/infra"
)

// RunEntityReimportCommand handles the "entity reimport" command.
type RunEntityReimportCommand struct {
	DB          *sql.DB
	Driver      jokadb.Driver
	EntitiesDir string
	FilePath    string // relative path argument
	AutoConfirm bool
}

func (r RunEntityReimportCommand) Execute(ctx context.Context) error {
	lockAdapter := lockinfra.NewLockAdapter(r.Driver, r.DB)

	if err := lockAdapter.Acquire(ctx, "entity reimport"); err != nil {
		return err
	}
	defer lockAdapter.Release(ctx) //nolint:errcheck

	dbAdapter := newEntityAdapter(r.Driver, r.DB)

	if err := dbAdapter.EnsureTrackingTable(ctx); err != nil {
		return fmt.Errorf("ensuring tracking table: %w", err)
	}
	if err := dbAdapter.EnsureRowTrackingTable(ctx); err != nil {
		return fmt.Errorf("ensuring row tracking table: %w", err)
	}
	if err := dbAdapter.EnsureContentHashColumn(ctx); err != nil {
		return fmt.Errorf("ensuring content hash column: %w", err)
	}

	fullPath := filepath.Join(r.EntitiesDir, r.FilePath)

	if _, err := os.Stat(fullPath); err != nil {
		return fmt.Errorf("entity file not found: %s", fullPath)
	}

	synced, err := dbAdapter.IsEntitySynced(ctx, r.FilePath)
	if err != nil {
		return err
	}
	if !synced {
		return fmt.Errorf("entity file %q has never been synced; use 'entity sync' first", r.FilePath)
	}

	tracked, err := dbAdapter.GetTrackedRows(ctx, r.FilePath)
	if err != nil {
		return err
	}

	contentHash, err := app.HashFileContent(fullPath)
	if err != nil {
		return err
	}

	fmt.Println()
	color.Set(color.Bold)
	fmt.Println("Entity reimport:")
	color.Unset()
	color.Cyan("  File: %s", r.FilePath)
	color.Cyan("  Tracked rows to delete: %d", len(tracked))
	fmt.Println()

	if !r.AutoConfirm {
		if !shared.Confirm("Proceed with entity reimport? (only 'yes' will confirm): ") {
			color.Yellow("Entity reimport cancelled.")
			return nil
		}
	}

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}

	txAdapter := newEntityTxAdapter(r.Driver, tx, r.DB)

	err = app.ReimportEntityAction{
		DB:          txAdapter,
		FilePath:    r.FilePath,
		FullPath:    fullPath,
		ContentHash: contentHash,
	}.Execute(ctx)
	if err != nil {
		tx.Rollback() //nolint:errcheck
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	color.Green("\nEntity reimport complete: %s", r.FilePath)
	return nil
}
