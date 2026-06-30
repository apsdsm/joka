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
	// SkipLock skips advisory lock acquisition. Used when an outer command
	// (e.g. `joka reset`) already holds the lock.
	SkipLock bool
	// DryRun computes and prints the plan (inserts + before/after updates)
	// without applying anything or acquiring the advisory lock.
	DryRun bool
	// Force treats every tracked file as modified (re-applies its row updates)
	// regardless of whether its stored hash matches the current file. Genuinely
	// new files are still inserted as usual. The escape hatch for when change
	// detection is in doubt.
	Force bool
}

func (r RunEntitySyncCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON

	if r.Force && !jsonOut {
		color.Yellow("Forced re-sync: every tracked file will be re-applied regardless of its stored hash.")
	}

	if !r.SkipLock && !r.DryRun {
		lockAdapter := lockinfra.NewLockAdapter(r.Driver, r.DB)

		if err := lockAdapter.Acquire(ctx, "entity sync"); err != nil {
			if jsonOut {
				return shared.PrintErrorJSON(err)
			}
			return err
		}

		defer lockAdapter.Release(ctx) //nolint:errcheck
	}

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

	var pending []*domain.EntityFile  // new files to insert
	var modified []*domain.EntityFile // tracked files whose content changed

	for _, rel := range relPaths {
		fullPath := filepath.Join(r.EntitiesDir, rel)

		hash, err := app.HashFileContent(fullPath)
		if err != nil {
			if jsonOut {
				return shared.PrintErrorJSON(err)
			}
			return err
		}

		already, err := dbAdapter.IsEntitySynced(ctx, rel)
		if err != nil {
			if jsonOut {
				return shared.PrintErrorJSON(err)
			}
			return err
		}

		if already {
			dbHash, err := dbAdapter.GetEntityHash(ctx, rel)
			if err != nil {
				if jsonOut {
					return shared.PrintErrorJSON(err)
				}
				return err
			}

			// A stored hash that matches means the file is unchanged. An
			// empty stored hash (synced before hashing existed) is treated
			// as modified, matching `entity status`; the update path then
			// backfills the hash. --force overrides this so an unchanged
			// file is re-applied anyway.
			if !r.Force && dbHash != "" && dbHash == hash {
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
			file.ContentHash = hash
			modified = append(modified, file)
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
		file.ContentHash = hash

		pending = append(pending, file)
	}

	if len(pending) == 0 && len(modified) == 0 {
		if jsonOut {
			shared.PrintJSON(map[string]any{"status": "ok", "synced": []string{}, "updated": []string{}, "message": "all entity files already synced"})
			return nil
		}
		color.Green("All entity files already synced.")
		return nil
	}

	plan, err := app.PlanSyncAction{
		DB:       dbAdapter,
		Files:    pending,
		Modified: modified,
	}.Execute(ctx)
	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	if r.DryRun {
		if jsonOut {
			shared.PrintJSON(map[string]any{"status": "ok", "dry_run": true, "plan": planJSON(plan)})
			return nil
		}
		printPlan(plan)
		color.Yellow("\nDry run — no changes applied.")
		return nil
	}

	if !jsonOut {
		printPlan(plan)

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

	result, err := app.SyncEntitiesAction{
		DB:       txAdapter,
		Files:    pending,
		Modified: modified,
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

	syncedPaths := result.Synced
	if syncedPaths == nil {
		syncedPaths = []string{}
	}
	updatedPaths := result.Updated
	if updatedPaths == nil {
		updatedPaths = []string{}
	}

	if jsonOut {
		shared.PrintJSON(map[string]any{"status": "ok", "synced": syncedPaths, "updated": updatedPaths, "forced": r.Force, "plan": planJSON(plan)})
		return nil
	}

	fmt.Println()

	for _, path := range syncedPaths {
		color.Green("  Synced: %s", path)
	}

	for _, path := range updatedPaths {
		color.Green("  Updated: %s", path)
	}

	if r.Force {
		color.Green("\nForced entity re-sync complete. %d synced, %d updated.", len(syncedPaths), len(updatedPaths))
	} else {
		color.Green("\nEntity sync complete. %d synced, %d updated.", len(syncedPaths), len(updatedPaths))
	}

	return nil
}

// printPlan renders a SyncPlan as a human-readable preview: new rows to insert
// and per-column before/after diffs for modified files.
func printPlan(plan *app.SyncPlan) {
	red := color.New(color.FgRed)
	green := color.New(color.FgGreen)

	hasInserts := false
	for _, f := range plan.Inserts {
		if len(f.Rows) > 0 {
			hasInserts = true
			break
		}
	}

	if hasInserts {
		fmt.Println()
		color.Set(color.Bold)
		fmt.Println("Entity files to sync (new):")
		color.Unset()

		for _, f := range plan.Inserts {
			color.Cyan("  %s", f.Path)
			for _, row := range f.Rows {
				label := row.Table
				if row.RefID != "" {
					label = fmt.Sprintf("%s (_id %s)", row.Table, row.RefID)
				}
				green.Printf("    + %s\n", label)
				for _, v := range row.Values {
					if v.Note != "" {
						fmt.Printf("        %s: (%s)\n", v.Column, v.Note)
					} else {
						fmt.Printf("        %s: %s\n", v.Column, v.Value)
					}
				}
			}
		}
	}

	for _, f := range plan.Updates {
		fmt.Println()
		color.Set(color.Bold)
		fmt.Println("Entity files to update (modified):")
		color.Unset()

		color.Cyan("  %s", f.Path)
		if len(f.Rows) == 0 {
			fmt.Println("    (no field-level changes; sync will refresh the tracking hash only)")
			continue
		}

		for _, row := range f.Rows {
			color.Set(color.Bold)
			fmt.Printf("    ~ %s (%s=%d)\n", row.Table, row.PKColumn, row.PKValue)
			color.Unset()

			for _, c := range row.Changes {
				if c.Regenerated {
					fmt.Printf("        %s: (regenerated)\n", c.Column)
					continue
				}
				if c.Deferred {
					fmt.Printf("        %s:\n", c.Column)
					red.Printf("          - %s\n", c.Before)
					green.Printf("          + (lookup, resolved at apply time)\n")
					continue
				}
				fmt.Printf("        %s:\n", c.Column)
				red.Printf("          - %s\n", c.Before)
				green.Printf("          + %s\n", c.After)
			}
		}
	}
}

// planJSON converts a SyncPlan into plain maps/slices for JSON output.
func planJSON(plan *app.SyncPlan) map[string]any {
	inserts := make([]map[string]any, 0, len(plan.Inserts))
	for _, f := range plan.Inserts {
		rows := make([]map[string]any, 0, len(f.Rows))
		for _, row := range f.Rows {
			values := make([]map[string]any, 0, len(row.Values))
			for _, v := range row.Values {
				values = append(values, map[string]any{"column": v.Column, "value": v.Value, "note": v.Note})
			}
			rows = append(rows, map[string]any{"table": row.Table, "ref_id": row.RefID, "values": values})
		}
		inserts = append(inserts, map[string]any{"file": f.Path, "rows": rows})
	}

	updates := make([]map[string]any, 0, len(plan.Updates))
	for _, f := range plan.Updates {
		rows := make([]map[string]any, 0, len(f.Rows))
		for _, row := range f.Rows {
			changes := make([]map[string]any, 0, len(row.Changes))
			for _, c := range row.Changes {
				changes = append(changes, map[string]any{
					"column": c.Column, "before": c.Before, "after": c.After, "regenerated": c.Regenerated, "deferred": c.Deferred,
				})
			}
			rows = append(rows, map[string]any{
				"table": row.Table, "pk_column": row.PKColumn, "pk_value": row.PKValue, "changes": changes,
			})
		}
		updates = append(updates, map[string]any{"file": f.Path, "rows": rows})
	}

	return map[string]any{"inserts": inserts, "updates": updates}
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
