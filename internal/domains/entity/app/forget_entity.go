package app

import (
	"context"
	"fmt"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// ForgetEntityAction removes joka's tracking for an entity file — its
// joka_entities record and every joka_entity_rows entry pointing at it —
// without touching the rows those entries point at.
//
// It is the one thing sync deliberately will not do. Sync never deletes and
// never disowns: a tracked entity no file declares is reported and left alone,
// and its file's record stays in the state for ever, because the automatic
// cleanup (forgetEmptyFiles) only fires for a file whose entities moved
// somewhere else, not one that was deleted.
//
// So forget covers the states sync leaves standing:
//
//   - Retiring a seed. Delete the file, forget the tracking, and the rows stay
//     as ordinary application data joka no longer owns. Needs --force, since
//     the rows are live.
//   - An orphan: the file and its rows are both gone and the tracking outlived
//     them. Nothing is live, so no --force.
//   - A duplicate _id claim blocking a tracking upgrade, where two entity sets
//     were seeded into one database and one claim has to go.
type ForgetEntityAction struct {
	DB DBAdapter
	// State is what joka last applied. Plan reads it rather than the database,
	// so what the preview shows and what Execute removes come from one value.
	State    *domain.State
	FilePath string
	// Force allows forgetting rows that are still in the database. Without it
	// Execute refuses: dropping the tracking for a live row hands it to nobody,
	// which is worth confirming.
	//
	// The refusal predates adoption and used to be justified by duplication —
	// the next sync would insert a second copy. It no longer does; a file that
	// still declares the entity claims the row back by its unique key.
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
	if _, synced := a.State.FileHash(a.FilePath); !synced {
		return nil, fmt.Errorf("%w: %s", domain.ErrEntityNotSynced, a.FilePath)
	}

	// RowsInFile is in the order the rows were created, which is how the plan
	// reads even though deletion runs the other way.
	ordered := a.State.RowsInFile(a.FilePath)

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
			var err error
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

// Execute drops the file and its rows from the state. It returns the plan it
// acted on so the caller can report what was removed, and returns that plan
// alongside ErrRowsStillLive when rows are live and Force is not set — the
// caller is expected to show the rows from the plan rather than pack them into
// the error.
//
// It writes nothing itself. The state is the caller's, so forgetting several
// files edits one document and the caller saves it once; a multi-file forget is
// then one write rather than two statements per file.
func (a ForgetEntityAction) Execute(ctx context.Context) (*ForgetPlan, error) {
	plan, err := a.Plan(ctx)
	if err != nil {
		return nil, err
	}

	if plan.Live > 0 && !a.Force {
		return plan, fmt.Errorf("%w: %d of %d for %s; use --force to forget them anyway",
			domain.ErrRowsStillLive, plan.Live, len(plan.Rows), a.FilePath)
	}

	a.State.ForgetFile(a.FilePath)

	return plan, nil
}
