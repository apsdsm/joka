package app

import (
	"context"
	"errors"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// entityFile builds a declared file.
func entityFile(path string, entities ...domain.Entity) *domain.EntityFile {
	return &domain.EntityFile{Path: path, ContentHash: "hash-" + path, Entities: entities}
}

// col builds an entity with columns.
func col(table, refID string, columns map[string]any, children ...domain.Entity) domain.Entity {
	return domain.Entity{Table: table, RefID: refID, PKColumn: "id", Columns: columns, Children: children}
}

// applyAll plans and applies every given file as dirty, the way the sync
// command does. The apply takes its instructions from the plan, so a test that
// skipped the plan would be exercising something the command never does.
func applyAll(t *testing.T, db *mockDBAdapter, files ...*domain.EntityFile) *ApplyResult {
	t.Helper()

	dirty := make(map[string]bool, len(files))
	for _, f := range files {
		dirty[f.Path] = true
	}

	return applyPlanned(t, db, dirty, files...)
}

// applyPlanned is applyAll with the dirty set under the test's control, and the
// declaration winning every conflict.
func applyPlanned(t *testing.T, db *mockDBAdapter, dirty map[string]bool, files ...*domain.EntityFile) *ApplyResult {
	t.Helper()

	ctx := context.Background()

	plan, err := (PlanSyncAction{DB: db, State: db.state, Declared: files, Dirty: dirty}).Execute(ctx)
	if err != nil {
		t.Fatalf("planning: %v", err)
	}

	result, err := ApplySetAction{
		DB:       db,
		Backend:  db.backend(),
		Declared: files,
		Dirty:    dirty,
		Recreate: plan.Recreate,
		Adopted:  plan.Adopted,
		Write:    plan.ColumnsToWrite(),
	}.Execute(ctx)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return result
}

// trackedByRef indexes the mock's tracking.
func trackedByRef(db *mockDBAdapter) map[string]domain.TrackedRow {
	out := make(map[string]domain.TrackedRow)
	for _, row := range db.trackedRows() {
		out[row.RefID] = row
	}
	return out
}

func TestApplySetInsertsANewSet(t *testing.T) {
	db := newMockDBAdapter()

	result := applyAll(t, db, entityFile("a.yaml",
		col("users", "admin", map[string]any{"name": "Admin"},
			col("profiles", "admin_profile", map[string]any{"bio": "hi"}),
		),
	))

	if len(result.Inserted) != 2 || len(result.Updated) != 0 {
		t.Fatalf("expected 2 inserts and no updates, got %+v", result)
	}
	if len(db.insertedRows) != 2 {
		t.Errorf("expected 2 rows inserted, got %d", len(db.insertedRows))
	}
	if len(db.trackedRows()) != 2 {
		t.Errorf("expected 2 rows tracked, got %d", len(db.trackedRows()))
	}
}

func TestApplySetAddingAnEntityIsOneInsert(t *testing.T) {
	// The failure this whole model exists for. Under positional matching an
	// entity added mid-file shifted everything after it, sync refused, and the
	// remedy deleted and re-inserted every row the file owned.
	db := newMockDBAdapter()

	applyAll(t, db, entityFile("a.yaml",
		col("fields", "first", map[string]any{"label": "First"}),
		col("fields", "third", map[string]any{"label": "Third"}),
	))
	db.insertedRows = nil

	result := applyAll(t, db, entityFile("a.yaml",
		col("fields", "first", map[string]any{"label": "First"}),
		col("fields", "second", map[string]any{"label": "Second"}),
		col("fields", "third", map[string]any{"label": "Third"}),
	))

	if len(result.Inserted) != 1 || result.Inserted[0] != "second" {
		t.Fatalf("expected exactly the new entity inserted, got %+v", result.Inserted)
	}
	// The other two did not change, so nothing is written to them. Adding an
	// entity mid-file used to rewrite every row after it.
	if len(result.Updated) != 0 {
		t.Errorf("expected the unchanged rows left alone, got %+v", result.Updated)
	}

	// The row that shifted keeps its primary key, so anything referencing it
	// stays valid.
	tracked := trackedByRef(db)
	if tracked["third"].RowPK != 2 {
		t.Errorf("expected third to keep its original PK 2, got %d", tracked["third"].RowPK)
	}
	if tracked["third"].InsertionOrder != 2 {
		t.Errorf("expected third re-recorded at position 2, got %d", tracked["third"].InsertionOrder)
	}
}

func TestApplySetReorderIsANoOp(t *testing.T) {
	// Under positional matching this silently swapped the two rows' contents
	// when they had no _id, and refused when they did.
	db := newMockDBAdapter()

	applyAll(t, db, entityFile("a.yaml",
		col("fields", "alpha", map[string]any{"label": "Alpha"}),
		col("fields", "beta", map[string]any{"label": "Beta"}),
	))
	before := trackedByRef(db)

	applyAll(t, db, entityFile("a.yaml",
		col("fields", "beta", map[string]any{"label": "Beta"}),
		col("fields", "alpha", map[string]any{"label": "Alpha"}),
	))
	after := trackedByRef(db)

	for _, ref := range []string{"alpha", "beta"} {
		if before[ref].RowPK != after[ref].RowPK {
			t.Errorf("%s: expected the same row, got PK %d then %d", ref, before[ref].RowPK, after[ref].RowPK)
		}
	}
	// Each entity was written back to its own row, so the values did not swap.
	for _, update := range db.updatedRows {
		want := map[int64]string{after["alpha"].RowPK: "Alpha", after["beta"].RowPK: "Beta"}
		if got := update.Columns["label"]; got != want[update.PKValue] {
			t.Errorf("row %d: expected label %q, got %v", update.PKValue, want[update.PKValue], got)
		}
	}
}

func TestApplySetMovingAnEntityBetweenFiles(t *testing.T) {
	db := newMockDBAdapter()

	applyAll(t, db,
		entityFile("a.yaml", col("fields", "alpha", map[string]any{"label": "Alpha"})),
		entityFile("b.yaml", col("fields", "beta", map[string]any{"label": "Beta"})),
	)
	originalPK := trackedByRef(db)["alpha"].RowPK

	// alpha moves from a.yaml to b.yaml.
	result := applyAll(t, db,
		entityFile("a.yaml"),
		entityFile("b.yaml",
			col("fields", "beta", map[string]any{"label": "Beta"}),
			col("fields", "alpha", map[string]any{"label": "Alpha"}),
		),
	)

	if len(result.Inserted) != 0 {
		t.Errorf("expected no inserts — the row already exists, got %+v", result.Inserted)
	}
	if len(result.Undeclared) != 0 {
		t.Errorf("expected nothing undeclared — alpha is still declared, got %+v", result.Undeclared)
	}
	if len(result.Moved) != 1 || result.Moved[0].RefID != "alpha" {
		t.Fatalf("expected alpha reported as moved, got %+v", result.Moved)
	}
	if result.Moved[0].From != "a.yaml" || result.Moved[0].To != "b.yaml" {
		t.Errorf("expected a.yaml → b.yaml, got %+v", result.Moved[0])
	}

	tracked := trackedByRef(db)
	if tracked["alpha"].RowPK != originalPK {
		t.Errorf("expected alpha to keep its row, got PK %d", tracked["alpha"].RowPK)
	}
	if tracked["alpha"].EntityFile != "b.yaml" {
		t.Errorf("expected the tracking re-pointed at b.yaml, got %q", tracked["alpha"].EntityFile)
	}
}

func TestApplySetRenamingAFileLeavesNothingBehind(t *testing.T) {
	db := newMockDBAdapter()

	applyAll(t, db, entityFile("old.yaml", col("fields", "alpha", map[string]any{"label": "Alpha"})))

	result := applyAll(t, db, entityFile("new.yaml", col("fields", "alpha", map[string]any{"label": "Alpha"})))

	if len(result.Inserted) != 0 {
		t.Errorf("expected a rename to insert nothing, got %+v", result.Inserted)
	}
	if len(result.ForgottenFiles) != 1 || result.ForgottenFiles[0] != "old.yaml" {
		t.Fatalf("expected the old path's record cleared, got %+v", result.ForgottenFiles)
	}
	if db.isTracked("old.yaml") {
		t.Error("expected the old file's record gone")
	}
	if trackedByRef(db)["alpha"].EntityFile != "new.yaml" {
		t.Error("expected the row re-pointed at the new path")
	}
}

func TestApplySetReportsUndeclaredWithoutDeleting(t *testing.T) {
	db := newMockDBAdapter()

	applyAll(t, db, entityFile("a.yaml",
		col("fields", "alpha", map[string]any{"label": "Alpha"}),
		col("fields", "beta", map[string]any{"label": "Beta"}),
	))

	result := applyAll(t, db, entityFile("a.yaml",
		col("fields", "alpha", map[string]any{"label": "Alpha"}),
	))

	if len(result.Undeclared) != 1 || result.Undeclared[0].RefID != "beta" {
		t.Fatalf("expected beta reported undeclared, got %+v", result.Undeclared)
	}
	// A seed file edited by mistake must not take data with it.
	if _, stillTracked := trackedByRef(db)["beta"]; !stillTracked {
		t.Error("expected beta's tracking left in place for a human to decide about")
	}
}

func TestApplySetRefusesATableChange(t *testing.T) {
	db := newMockDBAdapter()

	applyAll(t, db, entityFile("a.yaml", col("fields", "alpha", map[string]any{"label": "Alpha"})))

	// An _id names one row. A different table is a different thing wearing the
	// same name, and guessing which was meant would be worse than saying so.
	_, err := ApplySetAction{
		DB:       db,
		Backend:  db.backend(),
		Declared: []*domain.EntityFile{entityFile("a.yaml", col("widgets", "alpha", map[string]any{"label": "Alpha"}))},
		Dirty:    map[string]bool{"a.yaml": true},
	}.Execute(context.Background())

	if !errors.Is(err, domain.ErrEntityTableChanged) {
		t.Fatalf("expected ErrEntityTableChanged, got %v", err)
	}
}

func TestApplySetSkipsCleanFilesButStillCountsTheirDeclarations(t *testing.T) {
	db := newMockDBAdapter()

	a := entityFile("a.yaml", col("fields", "alpha", map[string]any{"label": "Alpha"}))
	b := entityFile("b.yaml", col("fields", "beta", map[string]any{"label": "Beta"}))
	applyAll(t, db, a, b)
	db.updatedRows = nil

	// Only b.yaml changed. a.yaml must not be written, but alpha must still
	// count as declared or it would be reported as undeclared.
	result, err := ApplySetAction{
		DB:       db,
		Backend:  db.backend(),
		Declared: []*domain.EntityFile{a, b},
		Dirty:    map[string]bool{"b.yaml": true},
	}.Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(result.Files) != 1 || result.Files[0] != "b.yaml" {
		t.Errorf("expected only b.yaml written, got %+v", result.Files)
	}
	if len(result.Undeclared) != 0 {
		t.Errorf("expected an unchanged file's entities still counted as declared, got %+v", result.Undeclared)
	}
	for _, update := range db.updatedRows {
		if update.PKValue == trackedByRef(db)["alpha"].RowPK {
			t.Error("expected the unchanged file's row untouched")
		}
	}
}

func TestApplySetResolvesAReferenceToAnotherFile(t *testing.T) {
	// _id is the identity, so a reference resolves wherever its target lives.
	// Files are processed in load order, which is what makes this predictable.
	db := newMockDBAdapter()

	result := applyAll(t, db,
		entityFile("01_parent.yaml", col("users", "admin", map[string]any{"name": "Admin"})),
		entityFile("02_child.yaml", col("profiles", "admin_profile", map[string]any{
			"user_id": "{{ admin.id }}",
		})),
	)

	if len(result.Inserted) != 2 {
		t.Fatalf("expected both inserted, got %+v", result.Inserted)
	}

	adminPK := trackedByRef(db)["admin"].RowPK
	for _, insert := range db.insertedRows {
		if insert.Table == "profiles" {
			if insert.Columns["user_id"] != adminPK {
				t.Errorf("expected user_id resolved to %d, got %v", adminPK, insert.Columns["user_id"])
			}
		}
	}
}

func TestApplySetResolvesAReferenceToAnAlreadyTrackedEntity(t *testing.T) {
	// The referenced entity is not written this run at all — its PK comes from
	// the tracking.
	db := newMockDBAdapter()

	parent := entityFile("01_parent.yaml", col("users", "admin", map[string]any{"name": "Admin"}))
	applyAll(t, db, parent)
	adminPK := trackedByRef(db)["admin"].RowPK
	db.insertedRows = nil

	child := entityFile("02_child.yaml", col("profiles", "admin_profile", map[string]any{
		"user_id": "{{ admin.id }}",
	}))

	if _, err := (ApplySetAction{
		DB:       db,
		Backend:  db.backend(),
		Declared: []*domain.EntityFile{parent, child},
		Dirty:    map[string]bool{"02_child.yaml": true},
	}).Execute(context.Background()); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(db.insertedRows) != 1 {
		t.Fatalf("expected only the child inserted, got %d", len(db.insertedRows))
	}
	if db.insertedRows[0].Columns["user_id"] != adminPK {
		t.Errorf("expected user_id %d from the tracking, got %v", adminPK, db.insertedRows[0].Columns["user_id"])
	}
}

func TestApplySetRecordsTheFileHash(t *testing.T) {
	db := newMockDBAdapter()
	file := entityFile("a.yaml", col("fields", "alpha", map[string]any{"label": "Alpha"}))

	applyAll(t, db, file)

	if hash, _ := db.fileHash("a.yaml"); hash != file.ContentHash {
		t.Errorf("expected the hash recorded, got %q", hash)
	}
}
