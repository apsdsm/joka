package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
	// Undeclared are tracked rows no file declares any more. Nothing would be
	// deleted on their account; they are here so a dry run reports them.
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
}

// IsConflict reports whether applying this change would discard something.
func (c ColumnChange) IsConflict() bool { return c.Verdict == VerdictConflict }

// HasChanges reports whether the plan has anything to do or say.
func (p *SyncPlan) HasChanges() bool {
	return len(p.Inserts) > 0 || len(p.Updates) > 0 ||
		len(p.Conflicts) > 0 || len(p.Undeclared) > 0
}

// ConflictedColumns counts the columns across every conflicted row.
func (p *SyncPlan) ConflictedColumns() int {
	n := 0
	for _, row := range p.Conflicts {
		n += len(row.Columns)
	}
	return n
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
}

// Execute builds the plan.
func (a PlanSyncAction) Execute(ctx context.Context) (*SyncPlan, error) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	plan := &SyncPlan{}

	refMap := make(map[string]int64, len(a.State.Entities))
	for refID, tracked := range a.State.Entities {
		refMap[refID] = tracked.PKValue
	}

	declared := make(map[string]bool)

	for _, file := range a.Declared {
		entities := flattenEntities(file.Entities, nil)
		for _, e := range entities {
			declared[e.RefID] = true
		}

		fp := FileInsertPlan{Path: file.Path}
		fup := FileUpdatePlan{Path: file.Path}

		for _, e := range entities {
			row, isTracked := a.State.Row(e.RefID)

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

			changes, err := ResolveRowChanges(ctx, a.DB, e, row, refMap, now, a.Dirty[file.Path])
			if err != nil {
				return nil, fmt.Errorf("%s: previewing %s (_id %s): %w", file.Path, e.Table, e.RefID, err)
			}

			pushes, conflicts := splitByVerdict(changes)

			if len(pushes) > 0 {
				fup.Rows = append(fup.Rows, RowUpdatePlan{
					Table: e.Table, PKColumn: row.PKColumn, PKValue: row.PKValue, Changes: pushes,
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

	for _, row := range a.State.AllRows() {
		if !declared[row.RefID] {
			plan.Undeclared = append(plan.Undeclared, row)
		}
	}

	return plan, nil
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
// Three kinds of column are reported without a verdict, because there is no
// three-way comparison to make:
//
//   - `_once` columns are skipped entirely. The database owns them; sync will
//     not write them, so showing a change would promise an update that never
//     comes.
//   - A non-deterministic template (argon2id, now, asm.* secrets) produces a
//     new value every time, so joka cannot say what the column "should" hold
//     and cannot tell drift from regeneration. Its only signal is the
//     declaration moving, which is what fileChanged carries. Reported as
//     Regenerated, and only when the file changed — otherwise rewriting it on
//     every run would churn every {{ now }} column on every boot.
//   - A lookup whose target row does not exist yet is reported as Deferred
//     rather than failing: the row may be inserted earlier in the same sync,
//     which applies inserts before updates.
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
			if fileChanged {
				changes = append(changes, ColumnChange{Column: k, Regenerated: true, Verdict: VerdictPush})
			}
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
		if verdict == VerdictUnchanged {
			continue
		}

		// Show both sides in the same canonical form when they are JSON, so
		// the difference is visible instead of being buried in a disagreement
		// about key order.
		before, afterStr := alignForDisplay(normalizeValue(current[k]), normalizeValue(after))
		changes = append(changes, ColumnChange{
			Column: k, Before: before, After: afterStr, Verdict: verdict,
		})
	}

	return changes, nil
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
