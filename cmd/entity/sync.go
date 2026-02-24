package entity

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/fatih/color"
	"github.com/apsdsm/joka/cmd/shared"
	"github.com/apsdsm/joka/internal/domains/entity/app"
	"github.com/apsdsm/joka/internal/domains/entity/domain"
	"github.com/apsdsm/joka/internal/domains/entity/infra"
	lockinfra "github.com/apsdsm/joka/internal/domains/lock/infra"
)

// RunEntitySyncCommand handles the "entity sync" command. It discovers YAML
// entity files, checks which have already been synced, and inserts new entity
// graphs inside a transaction.
type RunEntitySyncCommand struct {
	DB          *sql.DB
	EntitiesDir string
	AutoConfirm bool
}

// Execute acquires an advisory lock, discovers entity files, parses them,
// previews which files will be synced, and inserts new graphs in a transaction.
func (r RunEntitySyncCommand) Execute(ctx context.Context) error {
	// Acquire advisory lock to prevent concurrent sync/migration runs.
	lockAdapter := lockinfra.NewMySQLLockAdapter(r.DB)

	if err := lockAdapter.Acquire(ctx, "entity sync"); err != nil {
		return err
	}

	defer lockAdapter.Release(ctx) //nolint:errcheck

	// Ensure the tracking table exists.
	dbAdapter := infra.NewMySQLDBAdapter(r.DB)

	if err := dbAdapter.EnsureTrackingTable(ctx); err != nil {
		return fmt.Errorf("ensuring tracking table: %w", err)
	}

	// Discover entity files.
	relPaths, err := infra.DiscoverEntityFiles(r.EntitiesDir)
	if err != nil {
		return err
	}

	if len(relPaths) == 0 {
		color.Yellow("No entity files found in %s.", r.EntitiesDir)
		return nil
	}

	// Parse all files and check which are already synced.
	var pending []*domain.EntityFile

	for _, rel := range relPaths {
		fullPath := filepath.Join(r.EntitiesDir, rel)

		file, err := app.ParseEntityAction{Path: fullPath}.Execute()
		if err != nil {
			return err
		}

		// Use the relative path as the tracking key.
		file.Path = rel

		already, err := dbAdapter.IsEntitySynced(ctx, rel)
		if err != nil {
			return err
		}

		if already {
			continue
		}

		pending = append(pending, file)
	}

	if len(pending) == 0 {
		color.Green("All entity files already synced.")
		return nil
	}

	// Preview files to sync.
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

	// Begin transaction for all inserts.
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}

	txAdapter := infra.NewMySQLTxDBAdapter(tx, r.DB)

	synced, err := app.SyncEntitiesAction{
		DB:    txAdapter,
		Files: pending,
	}.Execute(ctx)
	if err != nil {
		tx.Rollback() //nolint:errcheck
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	fmt.Println()

	for _, path := range synced {
		color.Green("  Synced: %s", path)
	}

	color.Green("\nEntity sync complete. %d file(s) synced.", len(synced))

	return nil
}
