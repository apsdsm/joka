package app

import (
	"context"
	"fmt"
	"sort"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// ForgetEntityAction removes joka's tracking for an entity file — its
// joka_entities record and every joka_entity_rows entry pointing at it —
// without touching the rows those entries point at.
//
// It exists for the two states a tracking row can end up in that no other
// command resolves: a file whose rows were deleted by hand elsewhere, so the
// tracking points at nothing; and an orphan, where the file was deleted along
// with its rows but the tracking outlived both. `entity reimport` cannot help
// with either — it deletes and re-inserts, which needs a file, and on the first
// case would re-create rows that were removed deliberately.
type ForgetEntityAction struct {
	DB       DBAdapter
	FilePath string
	// Force allows forgetting rows that are still in the database. Without it
	// Execute refuses, because dropping the tracking for a live row leaves a
	// row nothing owns and the next sync inserts a second copy.
	Force bool
}

// ForgetPlan is what forgetting a file would remove, and the state of the rows
// it points at.
type ForgetPlan struct {
	FilePath string      `json:"file"`
	Rows     []ForgetRow `json:"rows"`
	// Live is how many of the tracked rows are still in the database. Anything
	// above zero is what Force exists for.
	Live int `json:"live"`
}

// ForgetRow is one tracked row and what became of the row it points at.
type ForgetRow struct {
	Table    string `json:"table"`
	PKColumn string `json:"pk_column"`
	PKValue  int64  `json:"pk_value"`
	RefID    string `json:"ref_id,omitempty"`
	// Live is true when the row is still in the database.
	Live bool `json:"live"`
	// TableMissing is true when the table itself is gone, which makes the row
	// unreachable rather than merely deleted.
	TableMissing bool `json:"table_missing"`
}

// Plan reads what forgetting the file would remove. It writes nothing, so a
// caller can show it before asking for confirmation.
func (a ForgetEntityAction) Plan(ctx context.Context) (*ForgetPlan, error) {
	synced, err := a.DB.IsEntitySynced(ctx, a.FilePath)
	if err != nil {
		return nil, err
	}
	if !synced {
		return nil, fmt.Errorf("%w: %s", domain.ErrEntityNotSynced, a.FilePath)
	}

	tracked, err := a.DB.GetTrackedRows(ctx, a.FilePath)
	if err != nil {
		return nil, err
	}

	// GetTrackedRows returns reverse insertion order (for deletion); sort
	// ascending so the plan reads in the order the rows were created.
	ordered := make([]domain.TrackedRow, len(tracked))
	copy(ordered, tracked)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].InsertionOrder < ordered[j].InsertionOrder
	})

	plan := &ForgetPlan{FilePath: a.FilePath, Rows: make([]ForgetRow, 0, len(ordered))}

	// Several rows usually share a table.
	tableExists := make(map[string]bool)

	for _, row := range ordered {
		pkColumn := row.PKColumn
		if pkColumn == "" {
			pkColumn = "id"
		}

		entry := ForgetRow{
			Table:    row.TableName,
			PKColumn: pkColumn,
			PKValue:  row.RowPK,
			RefID:    row.RefID,
		}

		exists, cached := tableExists[row.TableName]
		if !cached {
			exists, err = a.DB.TableExists(ctx, row.TableName)
			if err != nil {
				return nil, err
			}
			tableExists[row.TableName] = exists
		}

		if !exists {
			entry.TableMissing = true
			plan.Rows = append(plan.Rows, entry)
			continue
		}

		live, err := a.DB.RowExists(ctx, row.TableName, pkColumn, row.RowPK)
		if err != nil {
			return nil, err
		}
		entry.Live = live
		if live {
			plan.Live++
		}

		plan.Rows = append(plan.Rows, entry)
	}

	return plan, nil
}

// Execute drops the tracking. It returns the plan it acted on so the caller can
// report what was removed, and returns that plan alongside ErrRowsStillLive
// when rows are live and Force is not set — the caller is expected to show the
// rows from the plan rather than pack them into the error.
func (a ForgetEntityAction) Execute(ctx context.Context) (*ForgetPlan, error) {
	plan, err := a.Plan(ctx)
	if err != nil {
		return nil, err
	}

	if plan.Live > 0 && !a.Force {
		return plan, fmt.Errorf("%w: %d of %d for %s; use --force to forget them anyway, or 'entity reimport' to replace them",
			domain.ErrRowsStillLive, plan.Live, len(plan.Rows), a.FilePath)
	}

	if err := a.DB.DeleteTrackedRows(ctx, a.FilePath); err != nil {
		return plan, fmt.Errorf("removing row tracking for %s: %w", a.FilePath, err)
	}

	if err := a.DB.DeleteEntityRecord(ctx, a.FilePath); err != nil {
		return plan, fmt.Errorf("removing the tracking record for %s: %w", a.FilePath, err)
	}

	return plan, nil
}
