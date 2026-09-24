package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// SyncPlan describes, without applying anything, what an entity sync would do:
// the rows it would insert for new files and the field-level changes it would
// make to modified files.
type SyncPlan struct {
	Inserts []FileInsertPlan
	Updates []FileUpdatePlan
	// Conflicts are the rows where the database moved. Applying the
	// declaration would discard a change joka did not make, so what happens to
	// them is the caller's decision — see OnConflict.
	Conflicts []RowConflict
	// Recreate are the _ids joka tracks whose row is no longer in the database.
	// Somebody deleted it, or a restore lost it. The apply inserts them again
	// and re-points the tracking, which is the whole job of a tool that makes
	// the database match the declaration.
	Recreate map[string]bool
	// Adopted are declared entities joka does not track that were found in the
	// database anyway, keyed by _id. They are planned as updates against the row
	// that was found, with no baseline, so the declaration is written over it.
	// The apply records the tracking.
	Adopted map[string]Adoption
	// Deletes are tracked entities no file declares any more whose row is still
	// in the database. The declaration is the desired state, so an entity it no
	// longer mentions is one joka is being told to stop owning, and owning it
	// means the row goes with it. Ordered children before parents.
	Deletes []domain.TrackedRow
	// Forgets are tracked entities no file declares any more whose row is
	// already gone. Nothing is written; the tracking is dropped. This is the
	// orphan that used to need `entity forget`.
	Forgets []domain.TrackedRow
	// Rekeyed are entities whose _id changed while the row stayed put: the new
	// _id adopted the row by its unique key, so the old tracking is dropped
	// rather than the row being deleted.
	Rekeyed []EntityMove
	// Moves are the declared `moved:` entries that will do something here: the
	// from: is still tracked. Already-applied entries are absent, silently.
	Moves []PlannedMove
	// Removals are the declared `removed:` entries that will do something: the
	// _id is still tracked here. An entry naming an _id joka does not track is
	// absent, silently, so the same file can be left in place until every
	// database has applied it.
	Removals []PlannedRemoval
	// Undeclared are tracked rows joka cannot match to a declaration at all,
	// because they carry no _id. Not knowing whether a file declares them is
	// different from knowing none does, so they are reported and left alone.
	Undeclared []domain.TrackedRow
}

// RowConflict is one tracked row whose database values moved out from under the
// declaration.
type RowConflict struct {
	File     string         `json:"file"`
	RefID    string         `json:"ref_id"`
	Table    string         `json:"table"`
	PKColumn string         `json:"pk_column"`
	PKValue  int64          `json:"pk_value"`
	Columns  []ColumnChange `json:"columns"`
}

// FileInsertPlan is the set of rows a new (untracked) file would insert.
type FileInsertPlan struct {
	Path string
	Rows []RowInsertPlan
}

// RowInsertPlan is a single row that would be inserted.
type RowInsertPlan struct {
	Table  string
	RefID  string
	Values []ColumnValue
}

// ColumnValue is a column and the value it would be set to. Note is set for
// values that cannot be shown concretely at plan time: "generated" for
// non-deterministic templates (argon2id, now, asm.* secrets), "ref <handle>" for a
// reference to another entity's not-yet-assigned PK, or "lookup, resolved at
// apply time" for a lookup whose target row doesn't exist yet (it may be
// inserted earlier in the same sync).
type ColumnValue struct {
	Column string
	Value  string
	Note   string
}

// FileUpdatePlan is the set of row changes a modified file would apply.
type FileUpdatePlan struct {
	Path string
	Rows []RowUpdatePlan
}

// RowUpdatePlan is a single tracked row and the columns that would change on it.
type RowUpdatePlan struct {
	RefID    string
	Table    string
	PKColumn string
	PKValue  int64
	Changes  []ColumnChange
}

// ColumnChange is a single column whose value would change. When Regenerated is
// true the value is derived from a non-deterministic template (or a secret
// reference, which is never displayed) and Before/After are not meaningful
// (the value is rewritten on every sync). When Deferred is
// true the value is a lookup whose target row doesn't exist yet (it may be
// inserted by this same sync, which applies inserts before updates) and After
// can only be resolved at apply time.
// The json tags match the keys `entity sync --dry-run` writes by hand in
// cmd/entity/sync.go, so a column change reads the same whichever command
// produced it.
type ColumnChange struct {
	Column      string `json:"column"`
	Before      string `json:"before,omitempty"`
	After       string `json:"after,omitempty"`
	Regenerated bool   `json:"regenerated,omitempty"`
	Deferred    bool   `json:"deferred,omitempty"`

	// Verdict is what the three-way comparison concluded: push when only the
	// declaration moved, conflict when the database moved and writing the
	// declared value would discard a change joka did not make.
	Verdict ColumnVerdict `json:"verdict,omitempty"`

	// LiveHash is the digest of the value the database holds, set whenever the
	// verdict is a conflict. Conceding the conflict records it as the new
	// baseline.
	//
	// It is carried rather than re-derived from Before, because Before is
	// canonicalised for display and, for a regenerated or secret column,
	// deliberately not shown at all.
	LiveHash string `json:"-"`
}

// IsConflict reports whether applying this change would discard something.
func (c ColumnChange) IsConflict() bool { return c.Verdict == VerdictConflict }

// HasChanges reports whether the plan has anything to do or say.
//
// An adoption counts even when every column of the adopted row already agrees.
// Nothing is written to the row, but joka takes ownership of it, and that has to
// be recorded or the next run adopts it all over again.
func (p *SyncPlan) HasChanges() bool {
	return len(p.Inserts) > 0 || len(p.Updates) > 0 ||
		len(p.Conflicts) > 0 || len(p.Undeclared) > 0 || len(p.Adopted) > 0 ||
		len(p.Deletes) > 0 || len(p.Forgets) > 0 ||
		len(p.Rekeyed) > 0 || len(p.Removals) > 0 || len(p.Moves) > 0
}

// PlannedRemoval is one `removed:` entry matched to the row it names.
type PlannedRemoval struct {
	Removal domain.Removal
	// File is where the entry was declared, for the report.
	File string
	// Row is the tracking it will drop, and the row it will delete unless the
	// entry says to keep it.
	Row domain.TrackedRow
}

// ConflictedColumns counts the columns across every conflicted row.
func (p *SyncPlan) ConflictedColumns() int {
	n := 0
	for _, row := range p.Conflicts {
		n += len(row.Columns)
	}
	return n
}

// ColumnsToWrite is the plan's answer to "what does the apply write", per _id.
//
// The apply used to decide for itself: it rewrote every column of every entity
// in a file whose hash had moved. That disagreed with the plan in two ways that
// mattered. It wrote nothing at all for a clean file, so a conflict resolved in
// the declaration's favour and a row somebody had deleted were both planned and
// then silently skipped. And it wrote every column rather than the changed
// ones, so a `{{ now }}` in a file edited for an unrelated reason was rewritten
// along with it.
//
// Taking the answer from the plan is also what makes `--on-conflict=ask` mean
// anything: the operator answered questions about this plan, so this plan is
// what has to be executed.
// A conflicted column is included too, and filtered back out by ApplySetAction's
// Keep. That is one rule instead of a policy argument, and it lands the four
// cases correctly: under --on-conflict=file Keep is empty so the declaration is
// written over the drift; under db every conflicted column is kept so none is;
// under ask only the conceded ones are; and fail never reaches the apply.
func (p *SyncPlan) ColumnsToWrite() map[string][]string {
	out := make(map[string][]string)

	for _, file := range p.Updates {
		for _, row := range file.Rows {
			if row.RefID == "" {
				continue
			}
			for _, c := range row.Changes {
				out[row.RefID] = append(out[row.RefID], c.Column)
			}
		}
	}

	for _, row := range p.Conflicts {
		for _, c := range row.Columns {
			out[row.RefID] = append(out[row.RefID], c.Column)
		}
	}

	return out
}

// splitByVerdict separates the columns sync would write from the ones where the
// database moved.
func splitByVerdict(changes []ColumnChange) (pushes, conflicts []ColumnChange) {
	for _, c := range changes {
		if c.IsConflict() {
			conflicts = append(conflicts, c)
			continue
		}
		pushes = append(pushes, c)
	}
	return pushes, conflicts
}

// PlanSyncAction computes a SyncPlan from the same declared set ApplySetAction
// applies, matching on _id the same way. It is read-only: it resolves template
// values and reads the current values of rows that would change, but writes
// nothing.
type PlanSyncAction struct {
	DB DBAdapter
	// State is what joka last applied, loaded once by the caller and shared
	// with the apply, so the plan cannot describe a database the apply does not
	// then act on.
	State *domain.State
	// Declared is every file in the set, in load order.
	Declared []*domain.EntityFile
	// Dirty names the files whose content changed since the last sync.
	//
	// It no longer decides what gets compared: every declared entity is
	// compared against the database, which is what makes a row deleted or
	// edited out of band visible at all. It survives for the one kind of
	// column where the declaration moving is joka's only signal — a
	// non-deterministic template, whose value joka cannot predict and so
	// cannot tell drift from regeneration.
	Dirty map[string]bool
	// Decayed treats what the database holds as not worth comparing against:
	// every declared column of every declared entity is written, whatever is
	// there, and nothing is reported as a conflict.
	//
	// It is for the database whose seeded data has rotted — edited by hand over
	// a year, or restored from something older than the seeds — where the answer
	// is not to resolve the differences one at a time but to declare the files
	// authoritative and put them back. --on-conflict has no bearing on it,
	// because under decay there is nothing to have an opinion about.
	//
	// _once is still honoured. It names a column the application owns after
	// seeding, and a stale-seed sweep is not a reason to reset every password.
	Decayed bool
}

// Execute builds the plan.
func (a PlanSyncAction) Execute(ctx context.Context) (*SyncPlan, error) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	plan := &SyncPlan{}

	// One read of each table's unique indexes for the whole run, rather than one
	// per entity. See keyCache.
	keys := newKeyCache(a.DB)

	// Before anything reads the state: a move says what the state already was,
	// so the rest of the plan has to see the post-move world.
	plan.Moves = applyMoves(a.State, a.Declared)

	// A file declaring an _id goes before any file referencing it, so a
	// {{ ref.id }} resolves wherever in the set its target is declared. The
	// applier orders through the same function, or the plan would describe
	// inserts in an order the apply does not use.
	ordered, err := OrderFilesByReference(a.Declared)
	if err != nil {
		return nil, err
	}

	refMap := make(map[string]int64, len(a.State.Entities))
	for refID, tracked := range a.State.Entities {
		refMap[refID] = tracked.PKValue
	}

	declared := make(map[string]bool)

	for _, file := range ordered {
		entities := flattenEntities(file.Entities, nil)
		for _, e := range entities {
			declared[e.RefID] = true
		}

		fp := FileInsertPlan{Path: file.Path}
		fup := FileUpdatePlan{Path: file.Path}

		for i, e := range entities {
			row, isTracked := a.State.Row(e.RefID)

			// An entity joka does not track may still already be in the
			// database — someone seeded it before joka, or before joka tracked
			// rows by _id. Claim that row rather than inserting a second copy
			// on top of it, which is what the unique constraint used to stop
			// with a duplicate key error and no way forward.
			if !isTracked {
				adoption, adopted, err := adopt(ctx, keys, e, file.Path, i)
				if err != nil {
					return nil, fmt.Errorf("%s: looking for an existing %s (_id %s): %w",
						file.Path, e.Table, e.RefID, err)
				}
				if adopted {
					if plan.Adopted == nil {
						plan.Adopted = make(map[string]Adoption)
					}
					plan.Adopted[e.RefID] = adoption

					// An adopted row has a primary key, so a {{ ref.id }}
					// pointing at it resolves. Without this a child of an
					// adopted parent fails the plan with "not found in
					// reference map" — which is every parent-child seed on a
					// database joka is claiming for the first time.
					refMap[e.RefID] = adoption.Row.PKValue

					row, isTracked = adoption.Row, true
				}
			}

			if !isTracked {
				rip, err := a.planInsert(ctx, e, now)
				if err != nil {
					return nil, fmt.Errorf("%s: previewing: %w", file.Path, err)
				}
				fp.Rows = append(fp.Rows, rip)
				continue
			}

			// A table change is refused at apply time; the plan says so rather
			// than reading a row that does not describe this entity.
			if row.Table != e.Table {
				// No RefID: the apply refuses this entity, so it must not
				// reach ColumnsToWrite as something to write.
				fup.Rows = append(fup.Rows, RowUpdatePlan{
					Table: e.Table, PKColumn: row.PKColumn, PKValue: row.PKValue,
					Changes: []ColumnChange{{
						Column: "_is",
						Before: row.Table,
						After:  e.Table,
					}},
				})
				continue
			}

			changes, err := ResolveRowChanges(ctx, a.DB, e, row, refMap, now, a.Dirty[file.Path], a.Decayed)

			// The row joka tracks is gone. That is not a failure to read it —
			// it is the state of the database, and the answer is to put the
			// row back rather than to stop.
			if errors.Is(err, domain.ErrRowNotFound) {
				rip, planErr := a.planInsert(ctx, e, now)
				if planErr != nil {
					return nil, fmt.Errorf("%s: previewing: %w", file.Path, planErr)
				}
				fp.Rows = append(fp.Rows, rip)

				if plan.Recreate == nil {
					plan.Recreate = make(map[string]bool)
				}
				plan.Recreate[e.RefID] = true
				continue
			}

			if err != nil {
				return nil, fmt.Errorf("%s: previewing %s (_id %s): %w", file.Path, e.Table, e.RefID, err)
			}

			pushes, conflicts := splitByVerdict(changes)

			if len(pushes) > 0 {
				fup.Rows = append(fup.Rows, RowUpdatePlan{
					RefID: e.RefID, Table: e.Table, PKColumn: row.PKColumn, PKValue: row.PKValue, Changes: pushes,
				})
			}
			if len(conflicts) > 0 {
				plan.Conflicts = append(plan.Conflicts, RowConflict{
					File: file.Path, RefID: e.RefID, Table: e.Table,
					PKColumn: row.PKColumn, PKValue: row.PKValue, Columns: conflicts,
				})
			}
		}

		if len(fp.Rows) > 0 {
			plan.Inserts = append(plan.Inserts, fp)
		}
		if len(fup.Rows) > 0 {
			plan.Updates = append(plan.Updates, fup)
		}
	}

	a.planRemovals(plan)

	if err := a.planUndeclared(ctx, plan, declared); err != nil {
		return nil, err
	}

	return plan, nil
}

// planUndeclared sorts the tracked rows no file declares any more into what
// happens to each.
//
// The declaration is the desired state, so an entity it no longer mentions is
// one joka is being told to stop owning. Still in the database: delete it.
// Already gone: drop the tracking, which is the orphan nothing used to clear
// without `entity forget`.
//
// A row with no _id is neither. joka cannot match it to a declaration at all,
// so "no file declares it" is not something it knows — it is something it
// cannot tell. Those are reported and left alone. Deleting them would mean the
// first sync after a version 1 database's upgrade removing every row written
// before joka recorded _ids, which is the data loss this whole model exists to
// prevent.
// planRemovals turns the declared `removed:` entries into what each will do.
//
// An entry naming an _id joka does not track does nothing and says nothing.
// That is the whole point: the same entry has to be applied once against every
// database, and the ones that have already applied it must stay quiet, or a
// file kept until every environment has caught up would report the same finding
// for ever.
func (a PlanSyncAction) planRemovals(plan *SyncPlan) {
	for _, file := range a.Declared {
		for _, removal := range file.Removed {
			row, tracked := a.State.Row(removal.RefID)
			if !tracked {
				continue
			}

			plan.Removals = append(plan.Removals, PlannedRemoval{
				Removal: removal,
				File:    file.Path,
				Row: domain.TrackedRow{
					EntityFile: row.File, TableName: row.Table, RowPK: row.PKValue,
					PKColumn: row.PKColumn, RefID: removal.RefID, InsertionOrder: row.Order,
				},
			})
		}
	}

	sort.Slice(plan.Removals, func(i, j int) bool {
		return plan.Removals[i].Removal.RefID < plan.Removals[j].Removal.RefID
	})
}

func (a PlanSyncAction) planUndeclared(ctx context.Context, plan *SyncPlan, declared map[string]bool) error {
	// An _id a removal names is that removal's business. Without this it would
	// also read as undeclared, and the two paths would both act on one row.
	removing := make(map[string]bool)
	for _, file := range a.Declared {
		for _, removal := range file.Removed {
			removing[removal.RefID] = true
		}
	}

	// Every row an adoption claimed this run, by the _id that claimed it.
	claimed := make(map[string]string, len(plan.Adopted))
	for refID, adoption := range plan.Adopted {
		claimed[rowKey(adoption.Row.Table, adoption.Row.PKValue)] = refID
	}

	for _, row := range a.State.AllRows() {
		if declared[row.RefID] || removing[row.RefID] {
			continue
		}

		if row.RefID == "" {
			plan.Undeclared = append(plan.Undeclared, row)
			continue
		}

		live, err := a.DB.RowExists(ctx, row.TableName, row.PKColumn, row.RowPK)
		if err != nil {
			return fmt.Errorf("checking whether %s %s=%d is still there: %w",
				row.TableName, row.PKColumn, row.RowPK, err)
		}

		// The row another _id just claimed is not a row to delete: the entity
		// was renamed, and adoption found it again by its unique key. Deleting
		// it would destroy the row the new _id now tracks and leave the state
		// pointing at a primary key that no longer exists.
		//
		// This is the case terraform needs `moved` blocks for. joka can infer
		// it whenever the natural key stays put; a rename that also changes the
		// unique key is still a delete and an insert, and cannot be told apart
		// from one without being declared.
		if to, moved := claimed[rowKey(row.TableName, row.RowPK)]; moved {
			plan.Rekeyed = append(plan.Rekeyed, EntityMove{RefID: row.RefID, From: row.RefID, To: to})
			continue
		}

		if live {
			plan.Deletes = append(plan.Deletes, row)
			continue
		}
		plan.Forgets = append(plan.Forgets, row)
	}

	// Children before parents: a foreign key makes the order load-bearing, and
	// the insertion order is the only record of which is which.
	sort.Slice(plan.Deletes, func(i, j int) bool {
		if plan.Deletes[i].EntityFile != plan.Deletes[j].EntityFile {
			return plan.Deletes[i].EntityFile < plan.Deletes[j].EntityFile
		}
		return plan.Deletes[i].InsertionOrder > plan.Deletes[j].InsertionOrder
	})

	return nil
}

// planInsert describes one row that would be inserted. A template that cannot
// be resolved fails the plan rather than being reported as a note: the point of
// a dry run is to find out before applying, and a plan that quietly carried an
// unresolvable value would not do that. The exception is a lookup whose target
// row does not exist yet — it may be inserted earlier in the same run.
func (a PlanSyncAction) planInsert(ctx context.Context, e domain.Entity, now string) (RowInsertPlan, error) {
	rip := RowInsertPlan{Table: e.Table, RefID: e.RefID}

	for _, k := range sortedKeys(e.Columns) {
		raw := e.Columns[k]
		cv := ColumnValue{Column: k}

		switch {
		case isNonDeterministicTemplate(raw):
			cv.Note = "generated"
		default:
			if ref, ok := refTemplate(raw); ok {
				cv.Note = "ref " + ref
				break
			}
			val, err := resolveColumnValue(ctx, raw, nil, now, a.DB)
			if err != nil {
				if errors.Is(err, domain.ErrLookupNotFound) {
					cv.Note = "lookup, resolved at apply time"
					break
				}
				return rip, fmt.Errorf("%s.%s (_id %s): %w", e.Table, k, e.RefID, err)
			}
			cv.Value = normalizeValue(val)
		}

		rip.Values = append(rip.Values, cv)
	}

	return rip, nil
}

// resolveColumnValue resolves a single raw column value. Non-string values pass
// through unchanged; strings may be template expressions. No secret resolver is
// passed: secret references (asm.*) are non-deterministic to the planner and
// short-circuit before resolution, so their plaintext is never fetched or
// materialized into the plan.
func resolveColumnValue(ctx context.Context, raw any, refMap map[string]int64, now string, db DBAdapter) (any, error) {
	s, ok := raw.(string)
	if !ok {
		return raw, nil
	}
	return resolveValue(ctx, s, refMap, now, db, nil)
}

// normalizeValue renders a value as a string for comparison and display. Byte
// slices (some drivers' text representation) become strings; nil becomes the
// literal NULL.
func normalizeValue(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case []byte:
		return string(x)
	case string:
		return x
	default:
		return fmt.Sprintf("%v", x)
	}
}

// sortedKeys returns a map's keys in a stable alphabetical order so plan output
// is deterministic.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ResolveRowChanges compares one entity's declared columns against the row it
// is tracked against, and returns only the columns that differ, each carrying
// the verdict of a three-way comparison against the baseline.
//
// It reads the live row, so the row must exist.
//
// Two kinds of column are handled specially:
//
//   - `_once` columns are skipped entirely. The database owns them; sync will
//     not write them, so showing a change would promise an update that never
//     comes.
//   - A lookup whose target row does not exist yet is reported as Deferred
//     rather than failing: the row may be inserted earlier in the same sync,
//     which applies inserts before updates.
//
// A non-deterministic template (argon2id, now, asm.* secrets) gets half a
// comparison — see appendRegenerated.
//
// Shared by the sync preview (`entity sync --dry-run`) and `entity diff`, so
// the two can never disagree about what a column change is.
func ResolveRowChanges(
	ctx context.Context,
	db DBAdapter,
	e domain.Entity,
	row domain.EntityState,
	refMap map[string]int64,
	now string,
	fileChanged bool,
	// decayed treats every declared column as needing to be written, whatever
	// the database holds. See PlanSyncAction.Decayed.
	decayed bool,
) ([]ColumnChange, error) {
	cols := sortedKeys(e.Columns)

	pkColumn := row.PKColumn
	if pkColumn == "" {
		pkColumn = "id"
	}

	current, err := db.GetRow(ctx, e.Table, cols, pkColumn, row.PKValue)
	if err != nil {
		return nil, err
	}

	var changes []ColumnChange

	for _, k := range cols {
		raw := e.Columns[k]

		if e.IsOnce(k) {
			continue
		}

		if isNonDeterministicTemplate(raw) {
			// Under decay the comparison is skipped rather than run: the point
			// of the sweep is that what the database holds is not to be trusted,
			// and a regenerated column has no value to show either way.
			if decayed {
				changes = append(changes, ColumnChange{
					Column: k, Regenerated: true, Verdict: VerdictPush,
				})
				continue
			}
			changes = appendRegenerated(changes, k, current[k], row, fileChanged)
			continue
		}

		after, err := resolveColumnValue(ctx, raw, refMap, now, db)
		if err != nil {
			if errors.Is(err, domain.ErrLookupNotFound) {
				changes = append(changes, ColumnChange{
					Column: k, Before: normalizeValue(current[k]), Deferred: true, Verdict: VerdictPush,
				})
				continue
			}
			return nil, fmt.Errorf("%s.%s: %w", e.Table, k, err)
		}

		baseline, hasBaseline := row.Baseline(k)
		verdict := ClassifyColumn(after, current[k], baseline, hasBaseline)

		// Decay is the operator saying the database's copy of this data has
		// rotted and the files are the only version worth having. Every column
		// is written, and a column the database moved is not a conflict to
		// resolve — being wrong is the premise of the sweep.
		if decayed {
			verdict = VerdictPush
		}

		if verdict == VerdictUnchanged {
			continue
		}

		// Show both sides in the same canonical form when they are JSON, so
		// the difference is visible instead of being buried in a disagreement
		// about key order.
		before, afterStr := alignForDisplay(normalizeValue(current[k]), normalizeValue(after))
		changes = append(changes, ColumnChange{
			Column: k, Before: before, After: afterStr,
			Verdict: verdict, LiveHash: HashValue(current[k]),
		})
	}

	return changes, nil
}

// appendRegenerated compares a column whose declaration re-resolves to a new
// value on every run — {{ now }}, {{ argon2id|… }}, an asm.* secret.
//
// Half the three-way comparison still works, and it is the important half.
// The baseline holds the hash of what joka actually inserted — a concrete
// timestamp, a concrete argon2id digest — not the template, so comparing it
// against the live value says exactly whether the database moved. A password
// somebody reset is drift joka can see.
//
// What says nothing is declared-against-baseline: re-resolving the template
// produces a different value whether or not the author touched the file, so it
// always reads as changed. The declaration moving is the file hash, which is
// what fileChanged carries.
//
//	live == baseline  → the database holds what joka wrote. Rewrite it only
//	                    when the declaration moved, or every boot would churn
//	                    every created_at.
//	live != baseline  → the database moved. Conflict.
//	no baseline       → nothing to compare against; fall back to the file hash.
//
// Neither side's value is ever attached. A fresh hash tells the reader nothing,
// and an asm.* secret must not be printed — the planner goes out of its way not
// to resolve one.
func appendRegenerated(changes []ColumnChange, column string, live any, row domain.EntityState, fileChanged bool) []ColumnChange {
	liveHash := HashValue(live)

	if baseline, recorded := row.Baseline(column); recorded && liveHash != baseline {
		return append(changes, ColumnChange{
			Column: column, Regenerated: true,
			Verdict: VerdictConflict, LiveHash: liveHash,
		})
	}

	if !fileChanged {
		return changes
	}

	return append(changes, ColumnChange{Column: column, Regenerated: true, Verdict: VerdictPush})
}

// valuesEqual reports whether a declared value and the value in the database
// mean the same thing.
//
// Raw string comparison is not enough for JSON columns. PostgreSQL renders
// jsonb in its own key order with a space after each colon, while the YAML
// carries whatever the author typed — so a value nobody has touched compares
// unequal and the whole row reads as changed. When both sides parse as JSON,
// they are compared as canonicalised JSON (keys sorted, no insignificant
// whitespace) instead.
//
// Only both-sides-JSON is canonicalised. A plain string that merely looks like
// JSON on one side is compared raw, so nothing is silently reinterpreted.
func valuesEqual(before, after string) bool {
	if before == after {
		return true
	}

	beforeJSON, ok := canonicalJSON(before)
	if !ok {
		return false
	}
	afterJSON, ok := canonicalJSON(after)
	if !ok {
		return false
	}

	return beforeJSON == afterJSON
}

// canonicalJSON re-encodes a JSON document with sorted keys and no
// insignificant whitespace. It reports false for anything that is not a JSON
// object or array — a bare string, number or boolean round-trips through JSON
// unchanged, so treating those as JSON would buy nothing and risk equating
// values that differ only in quoting.
func canonicalJSON(s string) (string, bool) {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return "", false
	}

	var v any
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return "", false
	}

	// encoding/json sorts map keys on the way out, which is the canonical form
	// we want.
	out, err := json.Marshal(v)
	if err != nil {
		return "", false
	}

	return string(out), true
}

// alignForDisplay re-renders two differing values in the same canonical form
// when both are JSON, so a reader comparing them sees only what actually
// differs. Values that are not both JSON are returned unchanged.
func alignForDisplay(before, after string) (string, string) {
	beforeJSON, ok := canonicalJSON(before)
	if !ok {
		return before, after
	}
	afterJSON, ok := canonicalJSON(after)
	if !ok {
		return before, after
	}
	return beforeJSON, afterJSON
}

// rowKey identifies one database row for the move check: a table and a primary
// key, which is the only thing an adoption and a tracked row have in common
// when the _id between them has changed.
func rowKey(table string, pk int64) string {
	return table + "|" + strconv.FormatInt(pk, 10)
}
