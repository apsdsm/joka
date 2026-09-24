package app

import (
	"sort"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// PlannedMove is one `moved:` entry that will do something here.
type PlannedMove struct {
	Move domain.Move
	// File is where the entry was declared, for the report.
	File string
	// Row is the tracking it re-points. The row itself is untouched.
	Row domain.TrackedRow
}

// applyMoves re-points the tracking named by every `moved:` entry in the set,
// and returns the ones that did something.
//
// It runs before anything else reads the state, because a move is what the
// state *was* — reconciliation should see the post-move world, or the old _id
// reads as declared nowhere and the new one as untracked, which is a delete and
// an insert rather than a rename.
//
// The planner and the applier each call it against their own copy of the state:
// the planner's is the outer read that produces the report, the applier's is the
// one inside the transaction that gets saved. Running the same function over
// both is what stops the plan describing a move the apply then performs
// differently.
//
// An entry naming an _id joka does not track does nothing, silently — the same
// property a removal has, and for the same reason: one entry has to be applied
// once against each of several databases, and the ones already done must be
// quiet.
func applyMoves(state *domain.State, files []*domain.EntityFile) []PlannedMove {
	var applied []PlannedMove

	for _, file := range files {
		for _, move := range file.Moved {
			row, tracked := state.Row(move.From)
			if !tracked {
				continue
			}

			if !state.Rekey(move.From, move.To) {
				// The target is already tracked. Two _ids collapsing into one
				// would drop a row's tracking silently, so the move is left
				// undone and the set validation is what reports the clash.
				continue
			}

			applied = append(applied, PlannedMove{
				Move: move,
				File: file.Path,
				Row: domain.TrackedRow{
					EntityFile: row.File, TableName: row.Table, RowPK: row.PKValue,
					PKColumn: row.PKColumn, RefID: move.To, InsertionOrder: row.Order,
				},
			})
		}
	}

	sort.Slice(applied, func(i, j int) bool {
		return applied[i].Move.From < applied[j].Move.From
	})

	return applied
}
