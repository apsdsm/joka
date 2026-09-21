package entity

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/apsdsm/joka/cmd/shared"
	"github.com/apsdsm/joka/internal/domains/entity/app"
	"github.com/apsdsm/joka/internal/domains/entity/domain"
	"github.com/apsdsm/joka/internal/domains/entity/infra"
	lockinfra "github.com/apsdsm/joka/internal/domains/lock/infra"
	"github.com/fatih/color"
)

// RunEntitySyncCommand handles the "entity sync" command.
type RunEntitySyncCommand struct {
	DB *sql.DB
	// Secrets resolves {{ asm.<source>.<key> }} template references against the
	// `secrets:` sources in .jokarc.yaml.
	Secrets      app.SecretResolver
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

	fail := func(err error) error {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	if r.Force && !jsonOut {
		color.Yellow("Forced re-sync: every tracked file will be re-applied regardless of its stored hash.")
	}

	if !r.SkipLock && !r.DryRun {
		lockAdapter := lockinfra.NewPostgresLockAdapter(r.DB)

		if err := lockAdapter.Acquire(ctx, "entity sync"); err != nil {
			return fail(err)
		}

		defer lockAdapter.Release(ctx) //nolint:errcheck
	}

	dbAdapter := infra.NewPostgresDBAdapter(r.DB)

	if err := dbAdapter.EnsureTables(ctx); err != nil {
		return fail(err)
	}

	relPaths, err := infra.DiscoverEntityFiles(r.EntitiesDir)
	if err != nil {
		return fail(err)
	}

	if len(relPaths) == 0 {
		if jsonOut {
			shared.PrintJSON(map[string]any{"status": "ok", "synced": []string{}, "message": "no entity files found"})
			return nil
		}
		color.Yellow("No entity files found in %s.", r.EntitiesDir)
		return nil
	}

	// One read of the state for the whole run. The status of any one file
	// depends on the whole set anyway, because an _id can be claimed elsewhere.
	state, err := infra.NewPostgresStateBackend(r.DB).Load(ctx)
	if err != nil {
		return fail(err)
	}

	var pending []*domain.EntityFile  // new files to insert
	var modified []*domain.EntityFile // tracked files whose content changed
	var all []*domain.EntityFile      // every file, for set-level validation

	for _, rel := range relPaths {
		fullPath := filepath.Join(r.EntitiesDir, rel)

		hash, err := app.HashFileContent(fullPath)
		if err != nil {
			return fail(err)
		}

		// Every file is parsed, including ones the hash says are unchanged.
		// _id uniqueness is a property of the whole set, so an unchanged file
		// still has to be read to know what it claims. The hash decides
		// whether a file is written, not whether it is read.
		file, err := app.ParseEntityAction{Path: fullPath}.Execute()
		if err != nil {
			return fail(err)
		}
		file.Path = rel
		file.ContentHash = hash
		all = append(all, file)

		stored, tracked := state.FileHash(rel)

		// --force re-applies a tracked file whatever its stored hash says. It
		// does not make an untracked file anything other than new.
		switch app.FileStatusFor(tracked, stored, hash) {
		case domain.StatusNew:
			pending = append(pending, file)
		case domain.StatusModified:
			modified = append(modified, file)
		default:
			if r.Force {
				modified = append(modified, file)
			}
		}
	}

	// Validate the whole set before anything is written: an _id claimed twice
	// is only visible across files, and a set that cannot be identified is not
	// one joka should start writing from.
	if err := app.EntitySetError(app.ValidateEntitySet(all)); err != nil {
		return fail(err)
	}

	dirty := make(map[string]bool, len(pending)+len(modified))
	for _, f := range pending {
		dirty[f.Path] = true
	}
	for _, f := range modified {
		dirty[f.Path] = true
	}

	// The plan comes before the early return. A run with no dirty files can
	// still have something to say: deleting a file leaves every other file
	// unchanged, and the entities it declared are now declared nowhere.
	plan, err := app.PlanSyncAction{
		DB:       dbAdapter,
		State:    state,
		Declared: all,
		Dirty:    dirty,
	}.Execute(ctx)
	if err != nil {
		return fail(err)
	}

	if !plan.HasChanges() {
		if jsonOut {
			shared.PrintJSON(map[string]any{
				"status": "ok", "inserted": []string{}, "updated": []string{},
				"files": []string{}, "undeclared": []map[string]any{},
				"message": "all entity files already synced",
			})
			return nil
		}
		color.Green("All entity files already synced.")
		return nil
	}

	// Nothing to write, but tracked entities no file declares any more. Report
	// and stop: there is no transaction to open.
	if len(pending) == 0 && len(modified) == 0 {
		if jsonOut {
			shared.PrintJSON(map[string]any{
				"status": "ok", "inserted": []string{}, "updated": []string{},
				"files": []string{}, "undeclared": undeclaredJSON(plan.Undeclared),
			})
			return nil
		}
		reportUndeclared(plan.Undeclared)
		return nil
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
		return fail(fmt.Errorf("starting transaction: %w", err))
	}

	txAdapter := infra.NewPostgresTxDBAdapter(tx, r.DB)

	result, err := app.ApplySetAction{
		DB:       txAdapter,
		Backend:  infra.NewPostgresTxStateBackend(tx, r.DB),
		Secrets:  r.Secrets,
		Declared: all,
		Dirty:    dirty,
	}.Execute(ctx)
	if err != nil {
		tx.Rollback() //nolint:errcheck
		return fail(err)
	}

	if err := tx.Commit(); err != nil {
		return fail(fmt.Errorf("committing transaction: %w", err))
	}

	if jsonOut {
		shared.PrintJSON(map[string]any{
			"status": "ok", "forced": r.Force, "plan": planJSON(plan),
			"inserted": orEmpty(result.Inserted), "updated": orEmpty(result.Updated),
			"files": orEmpty(result.Files), "moved": result.Moved,
			"undeclared":      undeclaredJSON(result.Undeclared),
			"forgotten_files": orEmpty(result.ForgottenFiles),
		})
		return nil
	}

	fmt.Println()

	for _, path := range result.Files {
		color.Green("  Synced: %s", path)
	}

	for _, move := range result.Moved {
		color.Cyan("  Moved:  %s  %s → %s", move.RefID, move.From, move.To)
	}

	for _, path := range result.ForgottenFiles {
		color.Cyan("  Cleared tracking for %s (every entity it held moved elsewhere)", path)
	}

	fmt.Println()
	color.Green("Entity sync complete. %d inserted, %d updated across %d files.",
		len(result.Inserted), len(result.Updated), len(result.Files))

	// Nothing is deleted on an undeclared entity's account: a seed file edited
	// by mistake should not take data with it.
	reportUndeclared(result.Undeclared)

	fmt.Println()

	return nil
}

// orEmpty keeps a nil slice out of the JSON.
func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func firstRows(rows []domain.TrackedRow, n int) []domain.TrackedRow {
	if len(rows) <= n {
		return rows
	}
	return rows[:n]
}

// undeclaredJSON renders the tracked rows no file declares.
func undeclaredJSON(rows []domain.TrackedRow) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		out = append(out, map[string]any{
			"ref_id": row.RefID, "table": row.TableName,
			"pk_column": row.PKColumn, "pk_value": row.RowPK,
			"last_declared_in": row.EntityFile,
		})
	}
	return out
}
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

// reportUndeclared prints the tracked entities no file declares any more.
// Nothing is deleted on their account: a seed file edited by mistake should not
// take data with it.
func reportUndeclared(rows []domain.TrackedRow) {
	if len(rows) == 0 {
		return
	}

	fmt.Println()
	color.Yellow("%d tracked entities are no longer declared in any file:", len(rows))
	for _, row := range firstRows(rows, 10) {
		color.Yellow("  %s  %s %s %d  (last declared in %s)",
			row.RefID, row.TableName, row.PKColumn, row.RowPK, row.EntityFile)
	}
	if extra := len(rows) - 10; extra > 0 {
		color.Yellow("  … and %d more", extra)
	}
	fmt.Println()
	color.Yellow("  Nothing was deleted. 'joka entity forget <file>' drops the tracking,")
	color.Yellow("  'joka entity diff <file>' shows what each one points at.")
	fmt.Println()
}
