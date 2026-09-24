package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// movedFile is an entity file carrying only a `moved:` list.
func movedFile(path string, moves ...domain.Move) *domain.EntityFile {
	return &domain.EntityFile{Path: path, ContentHash: "hash", Moved: moves}
}

func TestMovedRepointsTheTrackingAndKeepsTheRow(t *testing.T) {
	// The rename joka cannot infer: the unique key changed too, so adoption
	// cannot find the row again and the rename is indistinguishable from a
	// delete plus an insert. Only the author can say which it was.
	db := newMockDBAdapter()
	db.uniqueKeys = map[string][][]string{"fields": {{"xid"}}}

	applyAll(t, db, entityFile("a.yaml", col("fields", "alpha", map[string]any{"xid": "x1"})))

	renamed := entityFile("a.yaml", col("fields", "beta", map[string]any{"xid": "x2"}))
	renamed.ContentHash = "renamed"
	moves := movedFile("_moved.yaml", domain.Move{From: "alpha", To: "beta"})

	// The plan is what the operator reads, so check it names the move. The mock
	// backend hands out one state object where production gives the apply its
	// own copy inside the transaction, so only one of the two applies it here.
	plan, err := (PlanSyncAction{
		DB: db, State: db.state, Declared: []*domain.EntityFile{renamed, moves},
		Dirty: map[string]bool{"a.yaml": true},
	}).Execute(context.Background())
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	if len(plan.Moves) != 1 || plan.Moves[0].Move.To != "beta" {
		t.Fatalf("expected the move planned, got %+v", plan.Moves)
	}
	if len(plan.Deletes) != 0 {
		t.Errorf("expected no delete alongside the move, got %+v", plan.Deletes)
	}

	result := applyAll(t, db, renamed, moves)

	if len(db.deletedRows) != 0 {
		t.Errorf("expected the row kept, got %+v", db.deletedRows)
	}
	if len(result.Inserted) != 0 {
		t.Errorf("expected no insert, got %v", result.Inserted)
	}

	tracked := trackedByRef(db)
	if _, gone := tracked["alpha"]; gone {
		t.Error("expected the old _id's record gone")
	}
	if beta, ok := tracked["beta"]; !ok || beta.RowPK != 1 {
		t.Errorf("expected beta on the original row, got %+v ok=%v", beta, ok)
	}
}

func TestAnAppliedMoveIsSilentAfterwards(t *testing.T) {
	db := newMockDBAdapter()
	db.uniqueKeys = map[string][][]string{"fields": {{"xid"}}}
	applyAll(t, db, entityFile("a.yaml", col("fields", "alpha", map[string]any{"xid": "x1"})))

	renamed := entityFile("a.yaml", col("fields", "beta", map[string]any{"xid": "x2"}))
	renamed.ContentHash = "renamed"
	moves := movedFile("_moved.yaml", domain.Move{From: "alpha", To: "beta"})
	applyAll(t, db, renamed, moves)

	plan, err := (PlanSyncAction{
		DB: db, State: db.state, Declared: []*domain.EntityFile{renamed, moves},
	}).Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(plan.Moves) != 0 {
		t.Errorf("expected an applied move to plan nothing, got %+v", plan.Moves)
	}
	if plan.HasChanges() {
		t.Errorf("expected nothing to report, got %+v", plan)
	}
}

func TestMoveTargetMustBeDeclared(t *testing.T) {
	// A typo in to: would move the tracking to a name nothing declares, and the
	// very next rule deletes a tracked entity no file declares. Requiring the
	// target to exist is what stops a typo being data loss.
	files := []*domain.EntityFile{
		entityFile("a.yaml", col("fields", "alpha", map[string]any{"xid": "x1"})),
		movedFile("_moved.yaml", domain.Move{From: "alpha", To: "btea"}),
	}

	err := EntitySetError(ValidateEntitySet(files))
	if !errors.Is(err, domain.ErrEntitySetInvalid) {
		t.Fatalf("expected the set refused, got %v", err)
	}
	if got := err.Error(); !strings.Contains(got, "btea") || !strings.Contains(got, "no entity declares") {
		t.Errorf("expected the target named, got:\n%s", got)
	}
}

func TestMoveSourceMustNotBeDeclared(t *testing.T) {
	// The file would be saying both "this entity exists" and "its record
	// belongs to another name".
	files := []*domain.EntityFile{
		entityFile("a.yaml",
			col("fields", "alpha", map[string]any{"xid": "x1"}),
			col("fields", "beta", map[string]any{"xid": "x2"}),
		),
		movedFile("_moved.yaml", domain.Move{From: "alpha", To: "beta"}),
	}

	err := EntitySetError(ValidateEntitySet(files))
	if !errors.Is(err, domain.ErrEntitySetInvalid) {
		t.Fatalf("expected the set refused, got %v", err)
	}
	if got := err.Error(); !strings.Contains(got, "still declared") {
		t.Errorf("expected the reason named, got:\n%s", got)
	}
}

func TestMoveWillNotCollapseTwoIDsIntoOne(t *testing.T) {
	// Both _ids tracked: re-keying would drop one row's record silently.
	state := domain.NewState()
	state.Track("alpha", domain.EntityState{Table: "fields", PKValue: 1})
	state.Track("beta", domain.EntityState{Table: "fields", PKValue: 2})

	if state.Rekey("alpha", "beta") {
		t.Fatal("expected the move refused")
	}
	if len(state.Entities) != 2 {
		t.Errorf("expected both records intact, got %+v", state.Entities)
	}
}
