package entity

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

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
	// StateFile overrides where the state file is written; empty means the
	// default beside the working directory.
	StateFile string
	// Profile and JokaVersion name the state file and stamp it.
	Profile     string
	JokaVersion string
	// SkipLock skips advisory lock acquisition. Used when an outer command
	// (e.g. `joka reset`) already holds the lock.
	SkipLock bool
	// DryRun computes and prints the plan (inserts + before/after updates)
	// without applying anything or acquiring the advisory lock.
	DryRun bool
	// Decayed declares the seeded data in the database stale and rewrites every
	// declared column. See app.PlanSyncAction.Decayed.
	Decayed bool
	// OnConflict decides what happens when the database moved out from under
	// the declaration. Defaults to refusing.
	OnConflict app.ConflictPolicy
}

func (r RunEntitySyncCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON

	fail := func(err error) error {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	if !r.SkipLock && !r.DryRun {
		lockAdapter := lockinfra.NewPostgresLockAdapter(r.DB)

		if err := lockAdapter.Acquire(ctx, "entity sync"); err != nil {
			return fail(err)
		}

		defer lockAdapter.Release(ctx) //nolint:errcheck
	}

	dbAdapter := infra.NewPostgresDBAdapter(r.DB)

	if err := infra.NewPostgresStateBackend(r.DB).EnsureStateTable(ctx); err != nil {
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

		// The hash no longer decides what gets reconciled — every declared
		// entity is compared against the database. It decides which files get
		// their content hash rewritten, and it is the only signal joka has for
		// a non-deterministic column.
		switch app.FileStatusFor(tracked, stored, hash) {
		case domain.StatusNew:
			pending = append(pending, file)
		case domain.StatusModified:
			modified = append(modified, file)
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
		Decayed:  r.Decayed,
	}.Execute(ctx)
	if err != nil {
		return fail(err)
	}

	if !plan.HasChanges() {
		// A file can be modified and still have nothing to apply: an edit that
		// makes the declaration match what the database already holds. Nothing
		// is written to the database, but the content hash still has to move,
		// or the file reads as modified on every run from here on.
		if len(dirty) > 0 {
			if err := refreshHashes(ctx, r, all, dirty); err != nil {
				return fail(err)
			}
		}

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
	// and stop: there is no transaction to open. An adoption is excluded: it
	// writes no row when every column agrees, but the tracking it records is a
	// write, and skipping it would claim the row again on every later run.
	if len(plan.Inserts) == 0 && len(plan.Updates) == 0 && len(plan.Conflicts) == 0 &&
		len(plan.Adopted) == 0 && len(plan.Deletes) == 0 && len(plan.Forgets) == 0 {
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

	// A conflict is the database holding a value joka did not write. Applying
	// the declaration over it would discard a change joka cannot account for,
	// so by default nothing is written and the run exits non-zero — which is
	// what makes entity sync a drift gate in CI.
	if len(plan.Conflicts) > 0 && r.OnConflict == app.ConflictFail {
		if !jsonOut {
			printPlan(plan)
			return app.ConflictSummary(plan.Conflicts)
		}
		return fail(app.ConflictError(plan.Conflicts))
	}

	// A conceded column is left alone in the database and its declaration is
	// rewritten to match. --on-conflict=db concedes every one of them;
	// --on-conflict=ask asks per column and builds the same answers from the
	// replies.
	keep := app.KeepFromConflicts(plan.Conflicts, r.OnConflict)
	resolutions := app.ResolutionsFor(plan.Conflicts, r.OnConflict)

	if len(plan.Conflicts) > 0 && r.OnConflict == app.ConflictAsk {
		if jsonOut || r.AutoConfirm {
			return fail(fmt.Errorf(
				"--on-conflict=ask needs someone to ask: use fail, file or db with --auto or --output json"))
		}

		printPlan(plan)

		var ok bool
		resolutions, ok = askConflicts(plan.Conflicts)
		if !ok {
			color.Yellow("Entity sync cancelled. Nothing was changed.")
			return nil
		}

		keep = app.KeepFromResolutions(plan.Conflicts, resolutions)
	}

	if !jsonOut {
		if r.OnConflict != app.ConflictAsk {
			printPlan(plan)
		}

		fmt.Println()

		for _, path := range app.FilesToRewrite(resolutions) {
			color.Cyan("  Will update the seed file to match the database: %s", path)
		}

		if !r.AutoConfirm {
			if !shared.Confirm("Proceed with entity sync? (only 'yes' will confirm): ") {
				color.Yellow("Entity sync cancelled.")
				return nil
			}
		}
	}

	// The seed files are rewritten after the confirmation and before the
	// database is touched. After, because a cancel that has already edited the
	// files on disk is not a cancel. Before, because if the write fails nothing
	// has been applied and the seeds are still what they were.
	var rewritten []string

	if len(resolutions) > 0 {
		rewritten, err = app.ApplyResolutions(r.EntitiesDir, resolutions)
		if err != nil {
			return fail(err)
		}

		// A rewritten file's content hash moved, and the copy in memory is the
		// one joka is about to record. Left stale, the next run would report
		// the file modified because of an edit joka made itself.
		if err := reloadFiles(r.EntitiesDir, all, rewritten); err != nil {
			return fail(err)
		}

		// A file joka has just rewritten is dirty by definition, and its hash has
		// to be refreshed with the rest. Without this the state keeps the hash
		// of the content from before joka's own edit, so the file reads as
		// modified on every later run — and a non-deterministic column in it is
		// regenerated every time, because the file hash is the only signal joka
		// has for those.
		for _, path := range rewritten {
			dirty[path] = true
		}

		if !jsonOut {
			for _, path := range rewritten {
				color.Cyan("  Updated the seed file to match the database: %s", path)
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
		Keep:     keep,
		Recreate: plan.Recreate,
		Adopted:  plan.Adopted,
		Delete:   plan.Deletes,
		Forget:   plan.Forgets,
		Write:    plan.ColumnsToWrite(),
	}.Execute(ctx)
	if err != nil {
		tx.Rollback() //nolint:errcheck
		return fail(err)
	}

	if err := tx.Commit(); err != nil {
		return fail(fmt.Errorf("committing transaction: %w", err))
	}

	if err := materializeState(ctx, r.DB, r.StateFile, r.Profile, r.JokaVersion); err != nil {
		color.Yellow("The sync committed, but the state file was not written: %v", err)
	}

	if jsonOut {
		shared.PrintJSON(map[string]any{
			"status": "ok", "on_conflict": string(r.OnConflict), "plan": planJSON(plan),
			"inserted": orEmpty(result.Inserted), "updated": orEmpty(result.Updated),
			"adopted":   orEmpty(result.Adopted),
			"deleted":   undeclaredJSON(result.Deleted),
			"forgotten": undeclaredJSON(result.Forgotten),
			"files":     orEmpty(result.Files), "moved": result.Moved,
			"undeclared":      undeclaredJSON(result.Undeclared),
			"forgotten_files": orEmpty(result.ForgottenFiles),
			"rewritten_files": orEmpty(rewritten),
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
		color.Cyan("  Cleared tracking for %s (it declares nothing joka still tracks)", path)
	}

	for _, refID := range result.Adopted {
		color.Yellow("  Claimed: %s (a row joka did not insert)", refID)
	}

	for _, row := range result.Deleted {
		color.Red("  Deleted: %s  %s %s %d (declared nowhere)",
			row.RefID, row.TableName, row.PKColumn, row.RowPK)
	}

	for _, row := range result.Forgotten {
		color.Cyan("  Dropped tracking for %s (its row was already gone)", row.RefID)
	}

	fmt.Println()
	color.Green("Entity sync complete. %d inserted, %d updated, %d claimed, %d deleted, across %d files.",
		len(result.Inserted), len(result.Updated), len(result.Adopted), len(result.Deleted), len(result.Files))

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

	printAdoptions(plan.Adopted)
	printDeletes(plan.Deletes, plan.Forgets)

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
				// A column being written whose value is not changing. Under
				// --decayed every declared column is written whatever it
				// holds, so most of them are this, and printing the same
				// string twice under a - and a + reads as a difference that
				// is not there.
				if c.Before == c.After {
					fmt.Printf("        %s: (rewritten, unchanged)\n", c.Column)
					continue
				}

				fmt.Printf("        %s:\n", c.Column)
				red.Printf("          - %s\n", c.Before)
				green.Printf("          + %s\n", c.After)
			}
		}
	}

	printConflicts(plan.Conflicts)
}

// printConflicts shows the rows the database moved, with the value it holds
// against the value the file declares, so the reader can pick a side without
// running a second command.
func printConflicts(conflicts []app.RowConflict) {
	if len(conflicts) == 0 {
		return
	}

	red := color.New(color.FgRed)
	green := color.New(color.FgGreen)

	fmt.Println()
	color.Set(color.Bold)
	fmt.Println("Changed in the database since joka last wrote:")
	color.Unset()

	for _, row := range conflicts {
		color.Yellow("  ! %s  %s %s %d  (%s)", row.RefID, row.Table, row.PKColumn, row.PKValue, row.File)

		for _, c := range row.Columns {
			if c.Regenerated {
				// A fresh hash tells the reader nothing, and an asm.* secret
				// must not be printed.
				fmt.Printf("        %s:\n", c.Column)
				red.Printf("          the database holds a value joka did not write\n")
				continue
			}
			fmt.Printf("        %s:\n", c.Column)
			red.Printf("          database %s\n", c.Before)
			green.Printf("          file     %s\n", c.After)
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

	conflicts := make([]map[string]any, 0, len(plan.Conflicts))
	for _, row := range plan.Conflicts {
		columns := make([]map[string]any, 0, len(row.Columns))
		for _, c := range row.Columns {
			columns = append(columns, map[string]any{
				"column": c.Column, "database": c.Before, "file": c.After,
			})
		}
		conflicts = append(conflicts, map[string]any{
			"file": row.File, "ref_id": row.RefID, "table": row.Table,
			"pk_column": row.PKColumn, "pk_value": row.PKValue, "columns": columns,
		})
	}

	return map[string]any{"inserts": inserts, "updates": updates, "conflicts": conflicts}
}

// reportUndeclared prints the tracked entities no file declares any more.
// Nothing is deleted on their account: a seed file edited by mistake should not
// take data with it.
func reportUndeclared(rows []domain.TrackedRow) {
	if len(rows) == 0 {
		return
	}

	fmt.Println()
	color.Yellow("%d tracked rows carry no _id, so joka cannot tell whether a file declares them:", len(rows))
	for _, row := range firstRows(rows, 10) {
		color.Yellow("  %s  %s %s %d  (last declared in %s)",
			row.RefID, row.TableName, row.PKColumn, row.RowPK, row.EntityFile)
	}
	if extra := len(rows) - 10; extra > 0 {
		color.Yellow("  … and %d more", extra)
	}
	fmt.Println()
	color.Yellow("  Nothing was deleted \u2014 an entity declared nowhere is removed, but these cannot be")
	color.Yellow("  matched either way. 'joka entity forget <file>' drops the tracking.")
	fmt.Println()
}

// refreshHashes records the content hash of files that changed without
// changing anything joka applies, and rewrites the state file.
//
// It is the no-op case of the same bookkeeping ApplySetAction does: there is no
// row to write, so there is no transaction's worth of work, but the hash still
// has to move.
func refreshHashes(
	ctx context.Context,
	r RunEntitySyncCommand,
	all []*domain.EntityFile,
	dirty map[string]bool,
) error {
	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting transaction: %w", err)
	}

	backend := infra.NewPostgresTxStateBackend(tx, r.DB)

	state, err := backend.Load(ctx)
	if err != nil {
		tx.Rollback() //nolint:errcheck
		return err
	}

	for _, file := range all {
		if dirty[file.Path] {
			state.TrackFile(file.Path, file.ContentHash)
		}
	}

	if err := backend.Save(ctx, state); err != nil {
		tx.Rollback() //nolint:errcheck
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	return materializeState(ctx, r.DB, r.StateFile, r.Profile, r.JokaVersion)
}

// printAdoptions names the rows joka found in the database and is about to
// claim, and what it matched each one on.
//
// It is printed rather than counted because a claim is a consequential thing to
// do quietly: the rows were put there by something other than joka, and the
// update listed below this will write the declaration over them. Someone
// reading "18 rows adopted" has to be able to check that `xid` is the column
// they would have matched on themselves.
func printAdoptions(adopted map[string]app.Adoption) {
	if len(adopted) == 0 {
		return
	}

	refIDs := make([]string, 0, len(adopted))
	for refID := range adopted {
		refIDs = append(refIDs, refID)
	}
	sort.Strings(refIDs)

	fmt.Println()
	color.Set(color.Bold)
	fmt.Println("Already in the database, and not tracked by joka — these rows will be claimed:")
	color.Unset()

	for _, refID := range refIDs {
		a := adopted[refID]
		color.Yellow("  = %s  %s %s %d  (%s, matched on %s)",
			refID, a.Row.Table, a.Row.PKColumn, a.Row.PKValue, a.File,
			strings.Join(a.MatchedOn, ", "))
	}
}

// printDeletes shows the rows a sync is about to remove because no file
// declares them any more, and the tracking it will drop for rows already gone.
//
// Deleting is the one thing a sync does that running it again cannot undo, so
// every row is named rather than counted. The confirmation prompt after the
// plan is the only gate on it.
func printDeletes(deletes, forgets []domain.TrackedRow) {
	if len(deletes) > 0 {
		red := color.New(color.FgRed)

		fmt.Println()
		color.Set(color.Bold)
		fmt.Println("Declared in no file any more — these rows will be DELETED:")
		color.Unset()

		for _, row := range deletes {
			red.Printf("  - %s  %s %s %d  (last declared in %s)\n",
				row.RefID, row.TableName, row.PKColumn, row.RowPK, row.EntityFile)
		}
	}

	if len(forgets) > 0 {
		fmt.Println()
		color.Set(color.Bold)
		fmt.Println("Declared in no file any more, and already gone from the database:")
		color.Unset()

		for _, row := range forgets {
			color.Cyan("  \u00b7 %s  %s %s %d  (tracking dropped, nothing to delete)",
				row.RefID, row.TableName, row.PKColumn, row.RowPK)
		}
	}
}
