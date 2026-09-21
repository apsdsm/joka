package app

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// ApplySetAction reconciles a declared entity set against what joka tracks,
// matching on _id.
//
// This replaces matching by (file, position). Under the old model an entity
// added mid-file shifted every entity after it, so the file no longer lined up
// with its tracked rows and sync refused — the remedy being a reimport that
// deleted and re-inserted every row the file owned. Adding a field to a seed
// file is the most ordinary edit there is, and it brought an application's boot
// down. Matching on _id makes it a plain insert.
//
// What each edit now costs:
//
//	add an entity        one INSERT
//	remove an entity     nothing is deleted; it is reported as undeclared
//	reorder entities     nothing, beyond re-recording the new positions
//	rename a file        nothing, beyond re-pointing the rows at the new path
//	move between files   nothing, same
//	edit a field         one UPDATE
type ApplySetAction struct {
	DB DBAdapter
	// Backend is where the state document is read and written. It is loaded
	// inside the caller's transaction and saved at the end of it, so the run
	// sees its own writes and a rollback takes the document with it.
	Backend StateBackend
	Secrets SecretResolver
	// Declared is every file in the set, in load order. Entities are matched
	// and written in that order, so a reference to another file's entity
	// resolves when that file sorts first — the same rule as within a file.
	Declared []*domain.EntityFile
	// Dirty names the files whose content changed since the last sync. It no
	// longer decides what gets written — every declared entity is reconciled —
	// but a non-deterministic column has no comparison to make, so the
	// declaration moving is the only signal joka has for it.
	Dirty map[string]bool
	// Keep names the columns the database owns for this run, per _id, mapped to
	// the baseline to record for each.
	//
	// The apply leaves every one of them alone. What differs is where the
	// concession is recorded. A column whose declaration was rewritten to match
	// maps to the empty string and keeps the baseline it had: joka did not write
	// it, so what it last applied has not moved, and the file no longer disagrees
	// anyway. A column whose declaration could not be rewritten — a template
	// expression, whose value joka cannot put in a file — maps to the hash of
	// what the database holds, and takes it as its new baseline. There is no file
	// edit that can settle that one, so recording it here is the only way the
	// operator's answer survives the run; otherwise the same difference is
	// reported forever.
	//
	// Taking the live hash for every conceded column, which is what this did at
	// first, is what made two runs of --on-conflict=db give opposite results: the
	// second run read live == baseline as an ordinary push and wrote the file's
	// value over the value just conceded.
	//
	// Empty under --on-conflict=file, where the declaration wins and every
	// column is written. Never reached under --on-conflict=fail, which refuses
	// before a transaction is opened.
	Keep map[string]map[string]string
	// Recreate names the _ids the plan found tracked but no longer in the
	// database. They are inserted again and the tracking is re-pointed at the
	// new row, rather than updated against a row that is not there.
	Recreate map[string]bool
	// Write is the plan's ColumnsToWrite: which columns of which _id to write.
	// An _id absent from it has nothing to write, so its row is left alone and
	// only its recorded position is refreshed.
	//
	// The apply used to decide this itself, by rewriting every column of every
	// entity in a file whose hash had moved. That disagreed with the plan
	// twice over: a clean file was skipped entirely, so a resolved conflict or
	// a deleted row was planned and then never written; and every column was
	// rewritten rather than the changed ones, so a {{ now }} moved whenever
	// anything else in its file did.
	Write map[string][]string
}

// ApplyResult reports what the run did.
type ApplyResult struct {
	// Inserted and Updated are the _ids written.
	Inserted []string `json:"inserted"`
	Updated  []string `json:"updated"`
	// Moved are entities whose tracking now points at a different file.
	Moved []EntityMove `json:"moved"`
	// Files are the paths written.
	Files []string `json:"files"`
	// Undeclared are tracked rows no file declares any more. Nothing is deleted
	// on their account — a seed file edited by mistake should not take data
	// with it — so they are reported for a human to decide about.
	Undeclared []domain.TrackedRow `json:"undeclared"`
	// ForgottenFiles are joka_entities records dropped because the file is gone
	// and every row it tracked now belongs to another file. This is what makes
	// a rename leave nothing behind.
	ForgottenFiles []string `json:"forgotten_files"`
}

// EntityMove records an entity that changed file.
type EntityMove struct {
	RefID string `json:"ref_id"`
	From  string `json:"from"`
	To    string `json:"to"`
}

// Execute applies the set. The caller is expected to wrap it in a transaction.
func (a ApplySetAction) Execute(ctx context.Context) (*ApplyResult, error) {
	result := &ApplyResult{}

	state, err := a.Backend.Load(ctx)
	if err != nil {
		return nil, err
	}

	// Every tracked row's primary key is available to {{ ref.id }} from the
	// start, so a reference resolves whether its target is being written this
	// run or was written some previous one.
	refMap := make(map[string]int64, len(state.Entities))
	for refID, tracked := range state.Entities {
		refMap[refID] = tracked.PKValue
	}

	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	declared := make(map[string]bool)

	for _, file := range a.Declared {
		entities := flattenEntities(file.Entities, nil)

		for _, e := range entities {
			declared[e.RefID] = true
		}

		for i, e := range entities {
			row, isTracked := state.Row(e.RefID)

			// A tracked row somebody deleted is inserted again. The _id is the
			// identity; which primary key it happens to hold is not, so
			// re-pointing it at a new row is the same entity continuing.
			if isTracked && a.Recreate[e.RefID] {
				isTracked = false
			}

			if !isTracked {
				pk, applied, err := a.insert(ctx, e, refMap, now)
				if err != nil {
					return nil, err
				}

				state.Track(e.RefID, domain.EntityState{
					Table:    e.Table,
					PKColumn: e.PKColumn,
					PKValue:  pk,
					File:     file.Path,
					Order:    i,
					Columns:  BaselineOf(applied),
				})

				refMap[e.RefID] = pk
				result.Inserted = append(result.Inserted, e.RefID)
				continue
			}

			// An _id names one row. If the entity now targets a different
			// table it is not that row any more, and guessing which of the two
			// the author meant would be worse than saying so.
			if row.Table != e.Table {
				return nil, fmt.Errorf("%w: _id %q is tracked as a row in %q but %s now declares it in %q; "+
					"use 'joka entity forget' to release the _id, or a different _id for the new row",
					domain.ErrEntityTableChanged, e.RefID, row.Table, file.Path, e.Table)
			}

			refMap[e.RefID] = row.PKValue

			columns, hasWork := a.Write[e.RefID]
			if !hasWork {
				// Nothing about this row changed. Its position may still have,
				// so the tracking is refreshed below without touching the row.
				a.retrack(state, result, e, row, file.Path, i)
				continue
			}

			applied, err := a.update(ctx, e, row, refMap, now, a.Keep[e.RefID], columns)
			if err != nil {
				return nil, err
			}
			result.Updated = append(result.Updated, e.RefID)

			if row.File != file.Path {
				result.Moved = append(result.Moved, EntityMove{
					RefID: e.RefID, From: row.File, To: file.Path,
				})
			}

			// The row was just rewritten, so what joka last applied is what it
			// applied a moment ago — for the columns it wrote. The rest keep
			// the baseline they had.
			row.File = file.Path
			row.Order = i
			row.Columns = baselineAfterUpdate(row.Columns, applied, a.Keep[e.RefID])
			state.Track(e.RefID, row)
		}

		// Only a file that was written gets its content hash refreshed. A
		// clean file's hash already matches, and a file whose entities were
		// all conceded to the database has not been made to agree with it.
		if a.Dirty[file.Path] {
			state.TrackFile(file.Path, file.ContentHash)
			result.Files = append(result.Files, file.Path)
		}
	}

	// Read after the writes rather than before: everything written this run is
	// declared by definition, so the answer is the same, and taking it from the
	// one document means it cannot disagree with what is about to be saved.
	for _, row := range state.AllRows() {
		if !declared[row.RefID] {
			result.Undeclared = append(result.Undeclared, row)
		}
	}

	result.ForgottenFiles = a.forgetEmptyFiles(state, result.Moved)

	if err := a.Backend.Save(ctx, state); err != nil {
		return nil, err
	}

	return result, nil
}

// insert resolves an entity's columns and inserts the row. It returns the
// resolved columns so the caller can record them as the baseline.
func (a ApplySetAction) insert(ctx context.Context, e domain.Entity, refMap map[string]int64, now string) (int64, map[string]any, error) {
	columns, err := resolveColumns(ctx, e.Columns, refMap, now, a.DB, a.Secrets)
	if err != nil {
		return 0, nil, fmt.Errorf("resolving %s (_id %s): %w", e.Table, e.RefID, err)
	}

	pk, err := a.DB.InsertRow(ctx, e.Table, columns, e.PKColumn)
	if err != nil {
		return 0, nil, fmt.Errorf("inserting %s (_id %s): %w", e.Table, e.RefID, err)
	}

	return pk, columns, nil
}

// update rewrites every column of the tracked row. All columns are written, not
// just changed ones, so a non-deterministic template produces a fresh value on
// every sync of a file that changed.
func (a ApplySetAction) update(
	ctx context.Context,
	e domain.Entity,
	row domain.EntityState,
	refMap map[string]int64,
	now string,
	kept map[string]string,
	planned []string,
) (map[string]any, error) {
	resolved, err := resolveColumns(ctx, e.Columns, refMap, now, a.DB, a.Secrets)
	if err != nil {
		return nil, fmt.Errorf("resolving %s (_id %s): %w", e.Table, e.RefID, err)
	}

	pkColumn := row.PKColumn
	if pkColumn == "" {
		pkColumn = "id"
	}

	// The plan said which columns differ; a _once column belongs to the
	// database from the moment it was seeded, and a kept one was conceded to it
	// for this run. Writing more than that is what made a password reset revert
	// and a {{ now }} move whenever anything else in its file did.
	writable := writableColumns(resolved, e, kept, planned)
	if len(writable) == 0 {
		return writable, nil
	}

	if err := a.DB.UpdateRow(ctx, e.Table, pkColumn, row.PKValue, writable); err != nil {
		return nil, fmt.Errorf("updating %s (_id %s): %w", e.Table, e.RefID, err)
	}

	return writable, nil
}

// retrack records where an entity is declared now, for a row nothing wrote to.
//
// An entity can move file or shift position without its values changing — an
// insert earlier in the file moves everything after it — and the record of
// where it was last declared is what error messages and `entity diff` read.
func (a ApplySetAction) retrack(
	state *domain.State,
	result *ApplyResult,
	e domain.Entity,
	row domain.EntityState,
	path string,
	position int,
) {
	if row.File == path && row.Order == position {
		return
	}

	if row.File != path {
		result.Moved = append(result.Moved, EntityMove{RefID: e.RefID, From: row.File, To: path})
	}

	row.File = path
	row.Order = position
	state.Track(e.RefID, row)
}

// baselineAfterUpdate merges what the update just wrote into the baseline the
// row already had.
//
// It merges rather than replaces because the baseline is the record of every
// column joka has applied, not of the last statement it ran. An update writes
// only the columns that differ, so replacing dropped the baseline of every
// column that happened to agree this run — and a column with no baseline is
// pushed over silently the next time the database moves it, which is the exact
// case the baseline exists to catch.
//
// Merging also covers the two kinds of column an update deliberately skips,
// with no special case: a _once column and a column conceded to the database
// are both absent from `applied`, so both keep the hash of what joka last
// applied to them. For a conceded column that is what keeps the concession
// stable. Recording the live hash instead reads as "joka applied this", so the
// next run sees live == baseline, calls it an ordinary push rather than a
// conflict, and writes the file's value over the value the operator had just
// said was right. Two runs of the same command gave opposite results.
func baselineAfterUpdate(
	previous map[string]string,
	applied map[string]any,
	kept map[string]string,
) map[string]string {
	out := make(map[string]string, len(previous)+len(applied))

	for name, hash := range previous {
		out[name] = hash
	}
	for name, hash := range BaselineOf(applied) {
		out[name] = hash
	}

	// A conceded column whose declaration could not be rewritten records the
	// database's hash. See Keep's doc comment: there is no file edit that can
	// settle it, so this is the only place the concession fits. An empty hash
	// means the declaration was rewritten instead, and the previous baseline
	// above is already right.
	for name, hash := range kept {
		if hash != "" {
			out[name] = hash
		}
	}

	return out
}

// writableColumns narrows a resolved row to the columns this run writes: the
// ones the plan found different, minus the ones the file declares `_once` and
// the ones a conflict was conceded to the database.
func writableColumns(resolved map[string]any, e domain.Entity, kept map[string]string, planned []string) map[string]any {
	out := make(map[string]any, len(planned))

	for _, name := range planned {
		if e.IsOnce(name) {
			continue
		}
		if _, held := kept[name]; held {
			continue
		}
		value, declared := resolved[name]
		if !declared {
			continue
		}
		out[name] = value
	}

	return out
}

// forgetEmptyFiles drops the record of a file that is no longer declared and no
// longer tracks any row — every entity it held moved somewhere else. Without
// this a rename leaves a permanent ghost entry behind, which nothing but a
// manual forget would ever clear.
//
// It reads the state after the run's writes, so "no longer tracks any row" is
// asked of the document about to be saved rather than of a second query.
func (a ApplySetAction) forgetEmptyFiles(state *domain.State, moved []EntityMove) []string {
	if len(moved) == 0 {
		return nil
	}

	sources := make(map[string]bool)
	for _, move := range moved {
		sources[move.From] = true
	}
	for _, file := range a.Declared {
		delete(sources, file.Path)
	}
	if len(sources) == 0 {
		return nil
	}

	for _, row := range state.AllRows() {
		delete(sources, row.EntityFile)
	}

	forgotten := make([]string, 0, len(sources))
	for path := range sources {
		state.ForgetFile(path)
		forgotten = append(forgotten, path)
	}

	sort.Strings(forgotten)
	return forgotten
}

// flattenEntities returns the entity graph in depth-first pre-order (parent
// before its children, siblings in order) — the order rows are inserted and
// their insertion_order recorded.
func flattenEntities(entities []domain.Entity, out []domain.Entity) []domain.Entity {
	for _, e := range entities {
		out = append(out, e)
		out = flattenEntities(e.Children, out)
	}
	return out
}
