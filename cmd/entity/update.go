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
	"github.com/apsdsm/joka/internal/domains/entity/domain"
	lockinfra "github.com/apsdsm/joka/internal/domains/lock/infra"
)

// RunEntityUpdateCommand handles the "entity update" command.
type RunEntityUpdateCommand struct {
	DB           *sql.DB
	Driver       jokadb.Driver
	EntitiesDir  string
	FilePath     string // relative path argument
	AutoConfirm  bool
	OutputFormat string
}

func (r RunEntityUpdateCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON

	lockAdapter := lockinfra.NewLockAdapter(r.Driver, r.DB)

	if err := lockAdapter.Acquire(ctx, "entity update"); err != nil {
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

	fullPath := filepath.Join(r.EntitiesDir, r.FilePath)

	if _, err := os.Stat(fullPath); err != nil {
		err = fmt.Errorf("entity file not found: %s", fullPath)
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	synced, err := dbAdapter.IsEntitySynced(ctx, r.FilePath)
	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}
	if !synced {
		err = fmt.Errorf("entity file %q has never been synced; use 'entity sync' first", r.FilePath)
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	// Get tracked rows to build preview.
	tracked, err := dbAdapter.GetTrackedRows(ctx, r.FilePath)
	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	trackedRefIDs := make(map[string]int64)
	trackedTables := make(map[string]string) // ref_id -> table
	for _, row := range tracked {
		if row.RefID != "" {
			trackedRefIDs[row.RefID] = row.RowPK
			trackedTables[row.RefID] = row.TableName
		}
	}

	// Parse YAML for preview.
	file, err := app.ParseEntityAction{Path: fullPath}.Execute()
	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	if err := app.ValidateRefIDs(file.Entities); err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	// Count new vs skipped for preview.
	type previewEntry struct {
		table string
		refID string
		pk    int64
		isNew bool
	}
	var preview []previewEntry
	var walkPreview func(entities []domain.Entity) error
	walkPreview = func(entities []domain.Entity) error {
		for _, e := range entities {
			if e.RefID == "" {
				return fmt.Errorf("all entities must have _id for entity update: table %q", e.Table)
			}
			if pk, ok := trackedRefIDs[e.RefID]; ok {
				preview = append(preview, previewEntry{
					table: e.Table,
					refID: e.RefID,
					pk:    pk,
					isNew: false,
				})
			} else {
				preview = append(preview, previewEntry{
					table: e.Table,
					refID: e.RefID,
					isNew: true,
				})
			}
			if err := walkPreview(e.Children); err != nil {
				return err
			}
		}
		return nil
	}

	if err := walkPreview(file.Entities); err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	newCount := 0
	skipCount := 0
	for _, p := range preview {
		if p.isNew {
			newCount++
		} else {
			skipCount++
		}
	}

	if newCount == 0 {
		if jsonOut {
			shared.PrintJSON(map[string]any{
				"status":   "ok",
				"file":     r.FilePath,
				"skipped":  len(preview),
				"inserted": 0,
				"message":  "nothing to update",
			})
			return nil
		}
		color.Green("Nothing to update — all entities in %s are already synced.", r.FilePath)
		return nil
	}

	contentHash, err := app.HashFileContent(fullPath)
	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	if !jsonOut {
		fmt.Println()
		color.Set(color.Bold)
		fmt.Printf("Entity update for %s:\n", r.FilePath)
		color.Unset()
		fmt.Println()

		for _, p := range preview {
			if p.isNew {
				color.Green("  [new]  %s (ref: %s)", p.table, p.refID)
			} else {
				color.White("  [skip] %s (ref: %s, pk: %d)", p.table, p.refID, p.pk)
			}
		}

		fmt.Println()
		fmt.Printf("%d to insert, %d unchanged.\n", newCount, skipCount)

		if !r.AutoConfirm {
			if !shared.Confirm("Proceed? (only 'yes' will confirm): ") {
				color.Yellow("Entity update cancelled.")
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

	result, err := app.UpdateEntityAction{
		DB:          txAdapter,
		FilePath:    r.FilePath,
		FullPath:    fullPath,
		ContentHash: contentHash,
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
		skippedJSON := make([]map[string]any, len(result.Skipped))
		for i, s := range result.Skipped {
			skippedJSON[i] = map[string]any{"table": s.Table, "ref_id": s.RefID, "pk": s.PK}
		}
		insertedJSON := make([]map[string]any, len(result.Inserted))
		for i, ins := range result.Inserted {
			insertedJSON[i] = map[string]any{"table": ins.Table, "ref_id": ins.RefID}
		}
		shared.PrintJSON(map[string]any{
			"status":   "ok",
			"file":     r.FilePath,
			"skipped":  skippedJSON,
			"inserted": insertedJSON,
		})
		return nil
	}

	color.Green("\nEntity update complete: %s (%d inserted, %d unchanged)", r.FilePath, len(result.Inserted), len(result.Skipped))
	return nil
}
