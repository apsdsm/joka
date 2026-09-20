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
	// Undeclared are tracked rows no file declares any more. Nothing would be
	// deleted on their account; they are here so a dry run reports them.
	Undeclared []domain.TrackedRow
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
}

// HasChanges reports whether the plan would actually do anything.
func (p *SyncPlan) HasChanges() bool {
	return len(p.Inserts) > 0 || len(p.Updates) > 0 || len(p.Undeclared) > 0
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
	// Dirty names the files that would be written.
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

		if !a.Dirty[file.Path] {
			continue
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

			changes, err := ResolveRowChanges(ctx, a.DB, e, row.PKColumn, row.PKValue, refMap, now)
			if err != nil {
				return nil, fmt.Errorf("%s: previewing %s (_id %s): %w", file.Path, e.Table, e.RefID, err)
			}
			if len(changes) > 0 {
				fup.Rows = append(fup.Rows, RowUpdatePlan{
					Table: e.Table, PKColumn: row.PKColumn, PKValue: row.PKValue, Changes: changes,
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
// is tracked against, and returns only the columns that would change.
//
// It reads the live row, so the row must exist. Non-deterministic templates
// (argon2id, now, asm.* secrets) are reported as Regenerated rather than
// compared, because they produce a new value on every sync and a hash-vs-hash
// diff would say nothing. A lookup whose target row does not exist yet is
// reported as Deferred rather than failing: the row may be inserted earlier in
// the same sync, which applies inserts before updates.
//
// Shared by the sync preview (`entity sync --dry-run`) and `entity diff`, so
// the two can never disagree about what a column change is.
func ResolveRowChanges(
	ctx context.Context,
	db DBAdapter,
	e domain.Entity,
	pkColumn string,
	pkValue int64,
	refMap map[string]int64,
	now string,
) ([]ColumnChange, error) {
	cols := sortedKeys(e.Columns)

	current, err := db.GetRow(ctx, e.Table, cols, pkColumn, pkValue)
	if err != nil {
		return nil, err
	}

	var changes []ColumnChange

	for _, k := range cols {
		raw := e.Columns[k]

		if isNonDeterministicTemplate(raw) {
			changes = append(changes, ColumnChange{Column: k, Regenerated: true})
			continue
		}

		after, err := resolveColumnValue(ctx, raw, refMap, now, db)
		if err != nil {
			if errors.Is(err, domain.ErrLookupNotFound) {
				changes = append(changes, ColumnChange{Column: k, Before: normalizeValue(current[k]), Deferred: true})
				continue
			}
			return nil, fmt.Errorf("%s.%s: %w", e.Table, k, err)
		}

		before := normalizeValue(current[k])
		afterStr := normalizeValue(after)
		if !valuesEqual(before, afterStr) {
			// Show both sides in the same canonical form when they are JSON,
			// so the difference is visible instead of being buried in a
			// disagreement about key order.
			before, afterStr = alignForDisplay(before, afterStr)
			changes = append(changes, ColumnChange{Column: k, Before: before, After: afterStr})
		}
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
