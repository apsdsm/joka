package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

func TestParseConflictPolicy(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want ConflictPolicy
	}{
		{"fail", ConflictFail},
		{"file", ConflictFile},
		{"db", ConflictDB},
		{"", ConflictFail},
	} {
		got, err := ParseConflictPolicy(tc.in)
		if err != nil {
			t.Errorf("%q: unexpected error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("%q: expected %q, got %q", tc.in, tc.want, got)
		}
	}

	if _, err := ParseConflictPolicy("yolo"); err == nil {
		t.Error("expected an unknown policy to be refused")
	}
}

// seeded applies a file once and then reports the plan for a second run, with
// the live row and the file's hash under the test's control.
func seeded(t *testing.T, db *mockDBAdapter, declared *domain.EntityFile) *SyncPlan {
	t.Helper()

	plan, err := (PlanSyncAction{
		DB:       db,
		State:    db.state,
		Declared: []*domain.EntityFile{declared},
		Dirty:    map[string]bool{declared.Path: db.dirty(declared)},
	}).Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	return plan
}

// dirty reports whether the file's hash differs from what the state records,
// the way the sync command decides it.
func (m *mockDBAdapter) dirty(f *domain.EntityFile) bool {
	stored, tracked := m.state.FileHash(f.Path)
	return FileStatusFor(tracked, stored, f.ContentHash) != domain.StatusSynced
}

func TestPlanReportsAConflictWhenTheDatabaseMoved(t *testing.T) {
	db := newMockDBAdapter()

	file := entityFile("a.yaml", col("fields", "alpha", map[string]any{"label": "Alpha"}))
	applyAll(t, db, file)

	// Somebody edited the row outside joka. The file has not changed.
	db.currentRows["fields|1"] = map[string]any{"label": "Edited in the app"}

	plan := seeded(t, db, file)

	if len(plan.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %+v", plan.Conflicts)
	}
	c := plan.Conflicts[0]
	if c.RefID != "alpha" || len(c.Columns) != 1 || c.Columns[0].Column != "label" {
		t.Errorf("expected the label column reported, got %+v", c)
	}
	if c.Columns[0].Before != "Edited in the app" || c.Columns[0].After != "Alpha" {
		t.Errorf("expected both sides shown, got %+v", c.Columns[0])
	}
	if len(plan.Updates) != 0 {
		t.Errorf("expected nothing queued to write, got %+v", plan.Updates)
	}
}

func TestPlanPushesWhenOnlyTheFileMoved(t *testing.T) {
	db := newMockDBAdapter()

	file := entityFile("a.yaml", col("fields", "alpha", map[string]any{"label": "Alpha"}))
	applyAll(t, db, file)
	db.currentRows["fields|1"] = map[string]any{"label": "Alpha"}

	changed := entityFile("a.yaml", col("fields", "alpha", map[string]any{"label": "Renamed"}))
	changed.ContentHash = "hash-changed"

	plan := seeded(t, db, changed)

	if len(plan.Conflicts) != 0 {
		t.Fatalf("expected no conflict, got %+v", plan.Conflicts)
	}
	if len(plan.Updates) != 1 || len(plan.Updates[0].Rows) != 1 {
		t.Fatalf("expected 1 update row, got %+v", plan.Updates)
	}
	if got := plan.Updates[0].Rows[0].Changes[0]; got.Verdict != VerdictPush {
		t.Errorf("expected a push, got %q", got.Verdict)
	}
}

func TestPlanComparesAFileThatDidNotChange(t *testing.T) {
	// The failure this whole phase is for: a row deleted or edited out of band
	// used to be invisible, because only a file whose hash moved was compared.
	db := newMockDBAdapter()

	file := entityFile("a.yaml", col("fields", "alpha", map[string]any{"label": "Alpha"}))
	applyAll(t, db, file)
	db.currentRows["fields|1"] = map[string]any{"label": "Alpha"}

	// Same file, same hash — and joka still notices nothing is wrong.
	if plan := seeded(t, db, file); plan.HasChanges() {
		t.Fatalf("expected a clean run to report nothing, got %+v", plan)
	}

	db.currentRows["fields|1"] = map[string]any{"label": "Edited in the app"}

	plan := seeded(t, db, file)
	if len(plan.Conflicts) != 1 {
		t.Fatalf("expected the drift reported on an unchanged file, got %+v", plan)
	}
}

func TestKeepFromConflicts(t *testing.T) {
	conflicts := []RowConflict{{
		RefID: "alpha",
		Columns: []ColumnChange{{
			Column: "label", Before: "Edited in the app", After: "Alpha",
			LiveHash: HashValue("Edited in the app"),
		}},
	}}

	t.Run("only the db policy produces one", func(t *testing.T) {
		if got := KeepFromConflicts(conflicts, ConflictFile); got != nil {
			t.Errorf("expected nothing kept when the file wins, got %+v", got)
		}
		if got := KeepFromConflicts(conflicts, ConflictFail); got != nil {
			t.Errorf("expected nothing kept when the run refuses, got %+v", got)
		}
	})

	t.Run("a rewritable column records no baseline of its own", func(t *testing.T) {
		got := KeepFromConflicts(conflicts, ConflictDB)

		// The column is held, but with nothing to record: its declaration is
		// rewritten to the database's value, so the difference is gone from the
		// file and the baseline still describes what joka last applied.
		if _, held := got["alpha"]["label"]; !held {
			t.Error("expected the column held")
		}
		if got["alpha"]["label"] != "" {
			t.Errorf("expected no baseline recorded, got %q", got["alpha"]["label"])
		}
	})

	t.Run("a column that cannot be rewritten records the database's hash", func(t *testing.T) {
		// There is no literal to write a {{ … }} declaration to, so the only
		// place this concession fits is the baseline. Without it the same
		// difference is reported on every run from here on.
		templated := []RowConflict{{
			RefID: "alpha",
			Columns: []ColumnChange{{
				Column: "password_hash", Regenerated: true,
				LiveHash: HashValue("reset by the user"),
			}},
		}}

		got := KeepFromConflicts(templated, ConflictDB)
		if got["alpha"]["password_hash"] != HashValue("reset by the user") {
			t.Errorf("expected the live value hashed, got %q", got["alpha"]["password_hash"])
		}
	})
}

func TestApplyKeepsTheDatabasesValue(t *testing.T) {
	db := newMockDBAdapter()

	file := entityFile("a.yaml", col("fields", "alpha", map[string]any{
		"label": "Alpha",
		"note":  "unchanged",
	}))
	applyAll(t, db, file)
	db.updatedRows = nil

	db.currentRows["fields|1"] = map[string]any{"label": "Edited in the app", "note": "unchanged"}

	changed := entityFile("a.yaml", col("fields", "alpha", map[string]any{
		"label": "Alpha",
		"note":  "edited in the file",
	}))
	changed.ContentHash = "hash-changed"

	plan := seeded(t, db, changed)
	if len(plan.Conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %+v", plan.Conflicts)
	}

	if _, err := (ApplySetAction{
		DB:       db,
		Backend:  db.backend(),
		Declared: []*domain.EntityFile{changed},
		Dirty:    map[string]bool{"a.yaml": true},
		Keep:     KeepFromConflicts(plan.Conflicts, ConflictDB),
		Write:    plan.ColumnsToWrite(),
	}).Execute(context.Background()); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(db.updatedRows) != 1 {
		t.Fatalf("expected 1 update, got %d", len(db.updatedRows))
	}
	written := db.updatedRows[0].Columns
	if _, wrote := written["label"]; wrote {
		t.Error("expected the conceded column left out of the update")
	}
	if written["note"] != "edited in the file" {
		t.Errorf("expected the other column still written, got %v", written["note"])
	}

	// The baseline still says what joka last applied. Recording the database's
	// value here instead is what made the next run read live == baseline as an
	// ordinary push and write the file's value back over the concession.
	alpha, _ := db.state.Row("alpha")
	if hash, _ := alpha.Baseline("label"); hash != HashValue("Alpha") {
		t.Errorf("expected the baseline left at what joka applied, got %q", hash)
	}

	// Until the declaration is rewritten the difference is still there, and
	// still reported. Conceding a literal is not finished until the file says so.
	if plan := seeded(t, db, changed); len(plan.Conflicts) != 1 {
		t.Errorf("expected the difference still reported, got %+v", plan.Conflicts)
	}

	// Rewriting it is what settles it — which is what ApplyResolutions does to
	// the file on disk under --on-conflict=db and ask.
	conceded := entityFile("a.yaml", col("fields", "alpha", map[string]any{
		"label": "Edited in the app",
		"note":  "edited in the file",
	}))
	conceded.ContentHash = "hash-conceded"

	if plan := seeded(t, db, conceded); len(plan.Conflicts) != 0 {
		t.Errorf("expected the conflict settled once the file agrees, got %+v", plan.Conflicts)
	}
}

func TestConflictError(t *testing.T) {
	if err := ConflictError(nil); err != nil {
		t.Errorf("expected no error for no conflicts, got %v", err)
	}

	err := ConflictError([]RowConflict{{
		File: "a.yaml", RefID: "alpha", Table: "fields", PKColumn: "id", PKValue: 1,
		Columns: []ColumnChange{{Column: "label", Before: "Edited", After: "Alpha"}},
	}})

	if !errors.Is(err, domain.ErrEntityConflict) {
		t.Fatalf("expected ErrEntityConflict, got %v", err)
	}
	for _, want := range []string{"alpha", "label", "Edited", "Alpha", "--on-conflict=file", "--on-conflict=db"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected %q in:\n%s", want, err)
		}
	}
}

func TestRegeneratedColumnDriftIsDetected(t *testing.T) {
	// The correction: a non-deterministic column still gets half a three-way
	// comparison, and it is the important half. The baseline holds the hash of
	// what joka actually inserted — a concrete argon2id digest, not the
	// template — so comparing it against the live value says exactly whether
	// the database moved.
	db := newMockDBAdapter()

	file := entityFile("a.yaml", col("users", "admin", map[string]any{
		"email":         "admin@example.com",
		"password_hash": "{{ argon2id|admin123 }}",
	}))
	applyAll(t, db, file)

	t.Run("an untouched row reports nothing, however often it is synced", func(t *testing.T) {
		if plan := seeded(t, db, file); plan.HasChanges() {
			t.Fatalf("expected a clean run, got %+v", plan)
		}
	})

	t.Run("a value the application changed is a conflict", func(t *testing.T) {
		db.currentRows["users|1"]["password_hash"] = "$argon2id$reset-by-the-app"

		plan := seeded(t, db, file)
		if len(plan.Conflicts) != 1 {
			t.Fatalf("expected the reset reported, got %+v", plan)
		}

		c := plan.Conflicts[0].Columns[0]
		if c.Column != "password_hash" || !c.Regenerated {
			t.Fatalf("expected the password column flagged as regenerated, got %+v", c)
		}
		// Neither side's value is attached: a fresh hash says nothing, and an
		// asm.* secret must not be printed.
		if c.Before != "" || c.After != "" {
			t.Errorf("expected no values carried, got before=%q after=%q", c.Before, c.After)
		}
		if c.LiveHash != HashValue("$argon2id$reset-by-the-app") {
			t.Errorf("expected the live hash carried for conceding, got %q", c.LiveHash)
		}
	})

	t.Run("conceding it stops the report without rewriting the column", func(t *testing.T) {
		plan := seeded(t, db, file)

		if _, err := (ApplySetAction{
			DB:       db,
			Backend:  db.backend(),
			Declared: []*domain.EntityFile{file},
			Dirty:    map[string]bool{"a.yaml": true},
			Keep:     KeepFromConflicts(plan.Conflicts, ConflictDB),
			Write:    plan.ColumnsToWrite(),
		}).Execute(context.Background()); err != nil {
			t.Fatalf("Execute: %v", err)
		}

		if got := db.currentRows["users|1"]["password_hash"]; got != "$argon2id$reset-by-the-app" {
			t.Errorf("expected the application's value left alone, got %v", got)
		}
		if plan := seeded(t, db, file); len(plan.Conflicts) != 0 {
			t.Errorf("expected the conflict settled, got %+v", plan.Conflicts)
		}
	})
}

func TestRegeneratedColumnIsRewrittenOnlyWhenTheFileMoved(t *testing.T) {
	db := newMockDBAdapter()

	build := func(hash string) *domain.EntityFile {
		f := entityFile("a.yaml", col("events", "first", map[string]any{
			"label":      "First",
			"created_at": "{{ now }}",
		}))
		f.ContentHash = hash
		return f
	}

	applyAll(t, db, build("hash-a"))
	db.updatedRows = nil

	// An unchanged file leaves it alone: rewriting on every run would churn
	// every created_at on every boot.
	if plan := seeded(t, db, build("hash-a")); plan.HasChanges() {
		t.Fatalf("expected an unchanged file to rewrite nothing, got %+v", plan)
	}

	// A changed file rewrites it, because the declaration moving is the only
	// signal joka has that the author meant something different.
	plan := seeded(t, db, build("hash-b"))
	if len(plan.Updates) != 1 {
		t.Fatalf("expected the changed file to queue an update, got %+v", plan.Updates)
	}
	found := false
	for _, c := range plan.Updates[0].Rows[0].Changes {
		if c.Column == "created_at" && c.Regenerated && c.Verdict == VerdictPush {
			found = true
		}
	}
	if !found {
		t.Errorf("expected created_at queued as a regenerated push, got %+v", plan.Updates[0].Rows[0].Changes)
	}
}

func TestPlanRecreatesADeletedRow(t *testing.T) {
	// The failure --force was added for and never fixed: a tracked row
	// somebody deleted used to make the whole run die reading a row that was
	// not there.
	db := newMockDBAdapter()
	file := entityFile("a.yaml", col("fields", "alpha", map[string]any{"label": "Alpha"}))
	applyAll(t, db, file)

	delete(db.currentRows, "fields|1")

	plan := seeded(t, db, file)

	if !plan.Recreate["alpha"] {
		t.Fatalf("expected alpha marked for re-creation, got %+v", plan.Recreate)
	}
	if len(plan.Inserts) != 1 || len(plan.Inserts[0].Rows) != 1 {
		t.Fatalf("expected the row planned as an insert, got %+v", plan.Inserts)
	}
	if len(plan.Conflicts) != 0 {
		t.Errorf("expected a missing row not reported as a conflict, got %+v", plan.Conflicts)
	}
}

func TestApplyRecreatesADeletedRow(t *testing.T) {
	db := newMockDBAdapter()
	file := entityFile("a.yaml", col("fields", "alpha", map[string]any{"label": "Alpha"}))
	applyAll(t, db, file)

	before, _ := db.state.Row("alpha")
	delete(db.currentRows, "fields|1")

	plan := seeded(t, db, file)

	if _, err := (ApplySetAction{
		DB:       db,
		Backend:  db.backend(),
		Declared: []*domain.EntityFile{file},
		Dirty:    map[string]bool{},
		Recreate: plan.Recreate,
		Adopted:  plan.Adopted,
		Delete:   plan.Deletes,
		Forget:   plan.Forgets,
	}).Execute(context.Background()); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	after, ok := db.state.Row("alpha")
	if !ok {
		t.Fatal("expected alpha still tracked")
	}

	// The _id is the identity; which primary key it holds is not, so the
	// tracking follows the new row.
	if after.PKValue == before.PKValue {
		t.Errorf("expected the tracking re-pointed at a new row, still at %d", after.PKValue)
	}
	if _, live := db.currentRows[fmt.Sprintf("fields|%d", after.PKValue)]; !live {
		t.Error("expected the row back in the database")
	}

	// And the run settles: nothing left to do.
	if plan := seeded(t, db, file); plan.HasChanges() {
		t.Errorf("expected the re-creation to settle, got %+v", plan)
	}
}

func TestAnUpdateKeepsTheBaselineOfTheColumnsItDidNotWrite(t *testing.T) {
	// The baseline is the record of every column joka has applied, not of the
	// last statement it ran. Replacing it with the columns of one update left
	// every other column with no baseline — and a column with no baseline is
	// pushed over silently the next time the database moves it, which is the
	// case the baseline exists to catch.
	db := newMockDBAdapter()

	file := entityFile("a.yaml", col("fields", "alpha", map[string]any{
		"label": "Alpha",
		"code":  "A",
	}))
	applyAll(t, db, file)

	edited := entityFile("a.yaml", col("fields", "alpha", map[string]any{
		"label": "Alpha renamed",
		"code":  "A",
	}))
	edited.ContentHash = "hash-edited"
	applyPlanned(t, db, map[string]bool{"a.yaml": true}, edited)

	alpha, _ := db.state.Row("alpha")
	if hash, recorded := alpha.Baseline("code"); !recorded || hash != HashValue("A") {
		t.Fatalf("expected the untouched column's baseline kept, got %q recorded=%v", hash, recorded)
	}

	// And it does its job: the database moving that column is a conflict, not a
	// difference joka assumes it is free to overwrite.
	db.currentRows["fields|1"] = map[string]any{"label": "Alpha renamed", "code": "B"}

	plan := seeded(t, db, edited)
	if len(plan.Conflicts) != 1 || plan.Conflicts[0].Columns[0].Column != "code" {
		t.Errorf("expected the drift on code reported as a conflict, got %+v", plan.Conflicts)
	}
}

func TestResolutionsFor(t *testing.T) {
	conflicts := []RowConflict{{
		File: "a.yaml", RefID: "admin",
		Columns: []ColumnChange{
			{Column: "email", Before: "moved@example.com", After: "admin@example.com",
				LiveHash: HashValue("moved@example.com")},
			{Column: "password_hash", Before: "", After: "", Regenerated: true,
				LiveHash: HashValue("reset by the user")},
		},
	}}

	t.Run("only the db policy answers anything", func(t *testing.T) {
		for _, policy := range []ConflictPolicy{ConflictFile, ConflictFail, ConflictAsk} {
			if got := ResolutionsFor(conflicts, policy); got != nil {
				t.Errorf("expected nothing answered under %s, got %+v", policy, got)
			}
		}
	})

	t.Run("db concedes every column and rewrites what it can", func(t *testing.T) {
		got := ResolutionsFor(conflicts, ConflictDB)
		if len(got) != 2 {
			t.Fatalf("expected an answer per column, got %+v", got)
		}

		for _, r := range got {
			if !r.KeepDatabase {
				t.Errorf("expected %s conceded to the database", r.Column)
			}
		}

		// Conceding means both leaving the value alone and making the
		// declaration say so. Doing only the first does not converge: the file
		// still disagrees, so the same conflict is reported on every run.
		if !got[0].UpdateFile || got[0].Value != "moved@example.com" {
			t.Errorf("expected the literal rewritten to the database's value, got %+v", got[0])
		}
		// There is no literal to write a regenerated column to.
		if got[1].UpdateFile {
			t.Error("expected the regenerated column left in the file")
		}
	})
}

func TestFilesToRewrite(t *testing.T) {
	// The confirmation prompt names the files it is about to change, and runs
	// before the change.
	got := FilesToRewrite([]Resolution{
		{File: "b.yaml", Column: "x", UpdateFile: true},
		{File: "a.yaml", Column: "y", UpdateFile: true},
		{File: "b.yaml", Column: "z", UpdateFile: true},
		{File: "c.yaml", Column: "w"},
	})

	if len(got) != 2 || got[0] != "a.yaml" || got[1] != "b.yaml" {
		t.Errorf("expected a.yaml and b.yaml once each, sorted, got %v", got)
	}
}
