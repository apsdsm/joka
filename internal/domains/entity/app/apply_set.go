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
	// Dirty names the files whose content changed since the last sync. Only
	// these are written. The rest still contribute their declarations, which is
	// what makes an _id claimed elsewhere and an entity declared nowhere both
	// visible.
	Dirty map[string]bool
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

		if !a.Dirty[file.Path] {
			continue
		}

		for i, e := range entities {
			row, isTracked := state.Row(e.RefID)

			if !isTracked {
				pk, err := a.insert(ctx, e, refMap, now)
				if err != nil {
					return nil, err
				}

				state.Track(e.RefID, domain.EntityState{
					Table:    e.Table,
					PKColumn: e.PKColumn,
					PKValue:  pk,
					File:     file.Path,
					Order:    i,
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

			if err := a.update(ctx, e, row, refMap, now); err != nil {
				return nil, err
			}
			refMap[e.RefID] = row.PKValue
			result.Updated = append(result.Updated, e.RefID)

			if row.File != file.Path || row.Order != i {
				if row.File != file.Path {
					result.Moved = append(result.Moved, EntityMove{
						RefID: e.RefID, From: row.File, To: file.Path,
					})
				}
				row.File = file.Path
				row.Order = i
				state.Track(e.RefID, row)
			}
		}

		state.TrackFile(file.Path, file.ContentHash)
		result.Files = append(result.Files, file.Path)
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

// insert resolves an entity's columns and inserts the row.
func (a ApplySetAction) insert(ctx context.Context, e domain.Entity, refMap map[string]int64, now string) (int64, error) {
	columns, err := resolveColumns(ctx, e.Columns, refMap, now, a.DB, a.Secrets)
	if err != nil {
		return 0, fmt.Errorf("resolving %s (_id %s): %w", e.Table, e.RefID, err)
	}

	pk, err := a.DB.InsertRow(ctx, e.Table, columns, e.PKColumn)
	if err != nil {
		return 0, fmt.Errorf("inserting %s (_id %s): %w", e.Table, e.RefID, err)
	}

	return pk, nil
}

// update rewrites every column of the tracked row. All columns are written, not
// just changed ones, so a non-deterministic template produces a fresh value on
// every sync of a file that changed.
func (a ApplySetAction) update(ctx context.Context, e domain.Entity, row domain.EntityState, refMap map[string]int64, now string) error {
	columns, err := resolveColumns(ctx, e.Columns, refMap, now, a.DB, a.Secrets)
	if err != nil {
		return fmt.Errorf("resolving %s (_id %s): %w", e.Table, e.RefID, err)
	}

	pkColumn := row.PKColumn
	if pkColumn == "" {
		pkColumn = "id"
	}

	if err := a.DB.UpdateRow(ctx, e.Table, pkColumn, row.PKValue, columns); err != nil {
		return fmt.Errorf("updating %s (_id %s): %w", e.Table, e.RefID, err)
	}

	return nil
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
