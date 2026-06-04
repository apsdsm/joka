package app

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// SyncPlan describes, without applying anything, what an entity sync would do:
// the rows it would insert for new files and the field-level changes it would
// make to modified files.
type SyncPlan struct {
	Inserts []FileInsertPlan
	Updates []FileUpdatePlan
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
// non-deterministic templates (argon2id, now), "ref <handle>" for a
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
// true the value is derived from a non-deterministic template and Before/After
// are not meaningful (the value is rewritten on every sync). When Deferred is
// true the value is a lookup whose target row doesn't exist yet (it may be
// inserted by this same sync, which applies inserts before updates) and After
// can only be resolved at apply time.
type ColumnChange struct {
	Column      string
	Before      string
	After       string
	Regenerated bool
	Deferred    bool
}

// HasChanges reports whether the plan would actually do anything.
func (p *SyncPlan) HasChanges() bool {
	return len(p.Inserts) > 0 || len(p.Updates) > 0
}

// PlanSyncAction computes a SyncPlan from the same new/modified file lists that
// SyncEntitiesAction applies. It is read-only: it resolves template values and
// reads the current values of rows that would be updated, but writes nothing.
type PlanSyncAction struct {
	DB       DBAdapter
	Files    []*domain.EntityFile // new files to insert
	Modified []*domain.EntityFile // modified files to update in place
}

// Execute builds the plan.
func (a PlanSyncAction) Execute(ctx context.Context) (*SyncPlan, error) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	plan := &SyncPlan{}

	for _, file := range a.Files {
		fp := FileInsertPlan{Path: file.Path}
		for _, e := range flattenEntities(file.Entities, nil) {
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
					// Deterministic value (plain, sha256, or lookup). Refs are
					// handled above, so resolution never needs the refMap here.
					val, err := resolveColumnValue(ctx, raw, nil, now, a.DB)
					if err != nil {
						// The plan runs before any inserts, so a lookup may
						// target a row this same sync is about to insert
						// (e.g. from another new file). Apply resolves it
						// after inserts; don't fail the plan over it.
						if errors.Is(err, domain.ErrLookupNotFound) {
							cv.Note = "lookup, resolved at apply time"
							break
						}
						return nil, fmt.Errorf("%s: previewing %s.%s: %w", file.Path, e.Table, k, err)
					}
					cv.Value = normalizeValue(val)
				}

				rip.Values = append(rip.Values, cv)
			}
			fp.Rows = append(fp.Rows, rip)
		}
		plan.Inserts = append(plan.Inserts, fp)
	}

	for _, file := range a.Modified {
		tracked, err := a.DB.GetTrackedRows(ctx, file.Path)
		if err != nil {
			return nil, err
		}

		seq, ordered, err := alignTrackedRows(file.Path, file.Entities, tracked)
		if err != nil {
			return nil, err
		}

		fup := FileUpdatePlan{Path: file.Path}
		refMap := make(map[string]int64)

		for i, e := range seq {
			row := ordered[i]
			cols := sortedKeys(e.Columns)

			current, err := a.DB.GetRow(ctx, e.Table, cols, row.PKColumn, row.RowPK)
			if err != nil {
				return nil, fmt.Errorf("%s: previewing %s: %w", file.Path, e.Table, err)
			}

			var changes []ColumnChange
			for _, k := range cols {
				raw := e.Columns[k]

				if isNonDeterministicTemplate(raw) {
					changes = append(changes, ColumnChange{Column: k, Regenerated: true})
					continue
				}

				after, err := resolveColumnValue(ctx, raw, refMap, now, a.DB)
				if err != nil {
					// Same as the insert path: the lookup target may be a row
					// inserted by this sync (inserts apply before updates), so
					// defer resolution to apply time instead of failing.
					if errors.Is(err, domain.ErrLookupNotFound) {
						changes = append(changes, ColumnChange{Column: k, Before: normalizeValue(current[k]), Deferred: true})
						continue
					}
					return nil, fmt.Errorf("%s: previewing %s.%s: %w", file.Path, e.Table, k, err)
				}

				before := normalizeValue(current[k])
				afterStr := normalizeValue(after)
				if before != afterStr {
					changes = append(changes, ColumnChange{Column: k, Before: before, After: afterStr})
				}
			}

			if e.RefID != "" {
				refMap[e.RefID] = row.RowPK
			}

			if len(changes) > 0 {
				fup.Rows = append(fup.Rows, RowUpdatePlan{
					Table:    e.Table,
					PKColumn: row.PKColumn,
					PKValue:  row.RowPK,
					Changes:  changes,
				})
			}
		}

		plan.Updates = append(plan.Updates, fup)
	}

	return plan, nil
}

// resolveColumnValue resolves a single raw column value. Non-string values pass
// through unchanged; strings may be template expressions.
func resolveColumnValue(ctx context.Context, raw any, refMap map[string]int64, now string, db DBAdapter) (any, error) {
	s, ok := raw.(string)
	if !ok {
		return raw, nil
	}
	return resolveValue(ctx, s, refMap, now, db)
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
