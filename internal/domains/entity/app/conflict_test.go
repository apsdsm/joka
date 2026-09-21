package app

import (
	"context"
	"errors"
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
		RefID:   "alpha",
		Columns: []ColumnChange{{Column: "label", Before: "Edited in the app", After: "Alpha"}},
	}}

	t.Run("only the db policy produces one", func(t *testing.T) {
		if got := KeepFromConflicts(conflicts, ConflictFile); got != nil {
			t.Errorf("expected nothing kept when the file wins, got %+v", got)
		}
		if got := KeepFromConflicts(conflicts, ConflictFail); got != nil {
			t.Errorf("expected nothing kept when the run refuses, got %+v", got)
		}
	})

	t.Run("it records the hash of what the database holds", func(t *testing.T) {
		got := KeepFromConflicts(conflicts, ConflictDB)

		// The next comparison computes this digest from the row itself, so it
		// has to match or the same conflict is reported forever.
		if got["alpha"]["label"] != HashValue("Edited in the app") {
			t.Errorf("expected the live value hashed, got %q", got["alpha"]["label"])
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

	// Conceding means the database's value becomes the baseline, so the same
	// difference is not reported again on the next run.
	alpha, _ := db.state.Row("alpha")
	if hash, _ := alpha.Baseline("label"); hash != HashValue("Edited in the app") {
		t.Errorf("expected the database's value recorded as the baseline, got %q", hash)
	}

	if plan := seeded(t, db, changed); len(plan.Conflicts) != 0 {
		t.Errorf("expected the conflict settled, got %+v", plan.Conflicts)
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
