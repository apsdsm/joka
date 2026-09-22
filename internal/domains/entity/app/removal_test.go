package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// removedFile is an entity file that declares nothing and removes one _id.
func removedFile(path string, removals ...domain.Removal) *domain.EntityFile {
	return &domain.EntityFile{Path: path, ContentHash: "hash", Removed: removals}
}

func TestRemovalWithKeepDropsTheTrackingAndLeavesTheRow(t *testing.T) {
	// The one thing an undeclared entity cannot get: joka stops owning the row
	// without deleting it. This is why the removal exists at all.
	db := newMockDBAdapter()
	applyAll(t, db, entityFile("a.yaml", col("fields", "alpha", map[string]any{"label": "Alpha"})))

	result := applyAll(t, db,
		removedFile("_removed.yaml", domain.Removal{RefID: "alpha", Keep: true}))

	if len(db.deletedRows) != 0 {
		t.Errorf("expected the row kept, got %+v", db.deletedRows)
	}
	if _, tracked := trackedByRef(db)["alpha"]; tracked {
		t.Error("expected the tracking dropped")
	}
	if len(result.Removed) != 1 || result.Removed[0].Removal.RefID != "alpha" {
		t.Errorf("expected the removal reported, got %+v", result.Removed)
	}
}

func TestRemovalWithoutKeepDeletesTheRow(t *testing.T) {
	db := newMockDBAdapter()
	applyAll(t, db, entityFile("a.yaml", col("fields", "alpha", map[string]any{"label": "Alpha"})))

	applyAll(t, db, removedFile("_removed.yaml", domain.Removal{RefID: "alpha"}))

	if len(db.deletedRows) != 1 || db.deletedRows[0].PKValue != 1 {
		t.Errorf("expected the row deleted, got %+v", db.deletedRows)
	}
	if _, tracked := trackedByRef(db)["alpha"]; tracked {
		t.Error("expected the tracking dropped")
	}
}

func TestAnAppliedRemovalIsSilentAfterwards(t *testing.T) {
	// The entry stays in the file because the same operation has to run against
	// every database. The ones that have already applied it must say nothing,
	// or a file kept until every environment catches up reports for ever.
	db := newMockDBAdapter()
	applyAll(t, db, entityFile("a.yaml", col("fields", "alpha", map[string]any{"label": "Alpha"})))

	removal := removedFile("_removed.yaml", domain.Removal{RefID: "alpha", Keep: true})
	applyAll(t, db, removal)

	plan, err := (PlanSyncAction{
		DB: db, State: db.state, Declared: []*domain.EntityFile{removal},
	}).Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(plan.Removals) != 0 {
		t.Errorf("expected an applied removal to plan nothing, got %+v", plan.Removals)
	}
	if plan.HasChanges() {
		t.Errorf("expected nothing to report, got %+v", plan)
	}
}

func TestAKeptRowIsNotAdoptedBackLater(t *testing.T) {
	// Keep needs no tombstone: adoption only looks for a *declared* entity, and
	// the premise of a removal is that nothing declares it any more.
	db := newMockDBAdapter()
	db.uniqueKeys = map[string][][]string{"fields": {{"xid"}}}
	applyAll(t, db, entityFile("a.yaml", col("fields", "alpha", map[string]any{"xid": "x1"})))

	removal := removedFile("_removed.yaml", domain.Removal{RefID: "alpha", Keep: true})
	applyAll(t, db, removal)

	plan, err := (PlanSyncAction{
		DB: db, State: db.state, Declared: []*domain.EntityFile{removal},
	}).Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(plan.Adopted) != 0 {
		t.Errorf("expected the released row left alone, got %+v", plan.Adopted)
	}
}

func TestAnIDCannotBeBothDeclaredAndRemoved(t *testing.T) {
	// The two say opposite things about one row, and guessing which the author
	// meant would be worse than refusing.
	files := []*domain.EntityFile{
		entityFile("a.yaml", col("fields", "alpha", map[string]any{"label": "Alpha"})),
		removedFile("_removed.yaml", domain.Removal{RefID: "alpha"}),
	}

	err := EntitySetError(ValidateEntitySet(files))
	if !errors.Is(err, domain.ErrEntitySetInvalid) {
		t.Fatalf("expected the set refused, got %v", err)
	}
	if got := err.Error(); !strings.Contains(got, "contradicts what the files declare") || !strings.Contains(got, "alpha") {
		t.Errorf("expected the _id and the reason named, got:\n%s", got)
	}
}
