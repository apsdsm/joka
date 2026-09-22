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
		Delete:   plan.Deletes,
		Forget:   plan.Forgets,
		Rekeyed:  plan.Rekeyed,
		Removals: plan.Removals,
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

func TestApplySetDeletesAnEntityDeclaredNowhere(t *testing.T) {
	// The declaration is the desired state, so an entity it no longer mentions
	// is one joka is being told to stop owning — and owning it means the row
	// goes with it. The gate is the plan and the confirmation, not a refusal.
	db := newMockDBAdapter()

	applyAll(t, db, entityFile("a.yaml",
		col("fields", "alpha", map[string]any{"label": "Alpha"}),
		col("fields", "beta", map[string]any{"label": "Beta"}),
	))

	result := applyAll(t, db, entityFile("a.yaml",
		col("fields", "alpha", map[string]any{"label": "Alpha"}),
	))

	if len(result.Deleted) != 1 || result.Deleted[0].RefID != "beta" {
		t.Fatalf("expected beta deleted, got %+v", result.Deleted)
	}
	if len(db.deletedRows) != 1 || db.deletedRows[0].PKValue != 2 {
		t.Errorf("expected beta's row deleted, got %+v", db.deletedRows)
	}
	if _, stillTracked := trackedByRef(db)["beta"]; stillTracked {
		t.Error("expected beta's tracking dropped with the row")
	}

	// alpha is untouched: only what the file stopped declaring goes.
	if _, stillTracked := trackedByRef(db)["alpha"]; !stillTracked {
		t.Error("expected alpha left alone")
	}
}

func TestApplySetNeverDeletesARowItCannotMatch(t *testing.T) {
	// A row with no _id cannot be matched to a declaration at all, so "no file
	// declares it" is not something joka knows — it is something it cannot
	// tell. Deleting those would make the first sync after a version 1
	// database's upgrade remove every row written before joka recorded _ids.
	db := newMockDBAdapter()
	db.track("a.yaml", domain.TrackedRow{TableName: "fields", RowPK: 7, PKColumn: "id", InsertionOrder: 0})
	db.currentRows["fields|7"] = map[string]any{"label": "from before _ids"}

	result := applyAll(t, db, entityFile("a.yaml", col("fields", "alpha", map[string]any{"label": "Alpha"})))

	if len(db.deletedRows) != 0 {
		t.Errorf("expected the unkeyed row left alone, got %+v", db.deletedRows)
	}
	if len(result.Undeclared) != 1 || result.Undeclared[0].RowPK != 7 {
		t.Errorf("expected it reported instead, got %+v", result.Undeclared)
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

func TestRenamingAnIDKeepsTheRow(t *testing.T) {
	// An _id rename where the natural key stays put: the new _id adopts the row
	// and the old one is declared nowhere. Before the move check, the adoption
	// and the delete named the same primary key — the row was destroyed and the
	// state was left pointing at a row that no longer existed.
	//
	// This is what terraform needs a `moved` block for. joka infers it whenever
	// the unique key does not change.
	db := newMockDBAdapter()
	db.uniqueKeys = map[string][][]string{"fields": {{"xid"}}}

	applyAll(t, db, entityFile("a.yaml",
		col("fields", "alpha", map[string]any{"xid": "x1", "label": "Alpha"}),
	))

	renamed := entityFile("a.yaml", col("fields", "beta", map[string]any{"xid": "x1", "label": "Alpha"}))
	renamed.ContentHash = "renamed"
	result := applyAll(t, db, renamed)

	if len(db.deletedRows) != 0 {
		t.Fatalf("expected nothing deleted on a rename, got %+v", db.deletedRows)
	}
	if len(result.Deleted) != 0 {
		t.Errorf("expected no deletes planned, got %+v", result.Deleted)
	}

	tracked := trackedByRef(db)
	if _, gone := tracked["alpha"]; gone {
		t.Error("expected the old _id's tracking dropped")
	}
	beta, ok := tracked["beta"]
	if !ok || beta.RowPK != 1 {
		t.Errorf("expected beta tracking the original row, got %+v ok=%v", beta, ok)
	}
}
