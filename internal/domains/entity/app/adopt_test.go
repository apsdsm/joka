package app

import (
	"context"
	"fmt"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// seededDB returns a database holding one fields row that joka does not track,
// with a unique index on xid — the shape of every database an existing joka
// user upgrades from.
func seededDB(t *testing.T) *mockDBAdapter {
	t.Helper()

	db := newMockDBAdapter()
	db.uniqueKeys = map[string][][]string{"fields": {{"xid"}}}
	db.currentRows["fields|7"] = map[string]any{
		"xid":   "field-one",
		"label": "Set up by hand",
	}
	return db
}

func TestSyncAdoptsARowItDidNotInsert(t *testing.T) {
	// Before adoption this was an insert, and the insert hit the unique
	// constraint the row was already occupying. tic_main's first sync under the
	// _id model died on exactly that: api_keys_xid_key.
	db := seededDB(t)

	file := entityFile("a.yaml", col("fields", "alpha", map[string]any{
		"xid":   "field-one",
		"label": "Declared",
	}))

	plan := seeded(t, db, file)

	if len(plan.Adopted) != 1 {
		t.Fatalf("expected the existing row adopted, got %+v", plan.Adopted)
	}
	adoption := plan.Adopted["alpha"]
	if adoption.Row.PKValue != 7 {
		t.Errorf("expected the row it found, got pk %d", adoption.Row.PKValue)
	}
	if len(adoption.MatchedOn) != 1 || adoption.MatchedOn[0] != "xid" {
		t.Errorf("expected the key it matched on reported, got %v", adoption.MatchedOn)
	}
	if len(plan.Inserts) != 0 {
		t.Errorf("expected nothing inserted over it, got %+v", plan.Inserts)
	}

	// Claimed, not merged with: the seed files are the desired state, so the
	// declaration is written over what the row holds, and the plan says so.
	if len(plan.Updates) != 1 || len(plan.Updates[0].Rows) != 1 {
		t.Fatalf("expected the declaration planned over it, got %+v", plan.Updates)
	}
	if changes := plan.Updates[0].Rows[0].Changes; len(changes) != 1 ||
		changes[0].Column != "label" || changes[0].After != "Declared" {
		t.Errorf("expected label pushed, got %+v", changes)
	}

	// A row joka did not write has no baseline, so nothing about it is a
	// conflict to resolve.
	if len(plan.Conflicts) != 0 {
		t.Errorf("expected no conflict on a row joka never wrote, got %+v", plan.Conflicts)
	}
}

func TestApplyRecordsAnAdoption(t *testing.T) {
	db := seededDB(t)

	file := entityFile("a.yaml", col("fields", "alpha", map[string]any{
		"xid":   "field-one",
		"label": "Declared",
	}))

	result := applyAll(t, db, file)

	if len(result.Adopted) != 1 || result.Adopted[0] != "alpha" {
		t.Errorf("expected the adoption reported, got %v", result.Adopted)
	}
	if len(result.Inserted) != 0 {
		t.Errorf("expected nothing inserted, got %v", result.Inserted)
	}

	alpha, tracked := db.state.Row("alpha")
	if !tracked {
		t.Fatal("expected the adopted row tracked")
	}
	if alpha.PKValue != 7 || alpha.Table != "fields" {
		t.Errorf("expected the tracking to name the row it claimed, got %+v", alpha)
	}

	// The second run is an ordinary tracked entity, with nothing to do.
	if plan := seeded(t, db, file); plan.HasChanges() {
		t.Errorf("expected the claim to settle it, got %+v", plan)
	}
}

func TestAnAgreeingRowIsStillClaimed(t *testing.T) {
	// "If the data is already there and it wholly agrees with me, I use it."
	// Nothing is written to the row, but the tracking has to be, or the next
	// run adopts it all over again.
	db := seededDB(t)

	file := entityFile("a.yaml", col("fields", "alpha", map[string]any{
		"xid":   "field-one",
		"label": "Set up by hand",
	}))

	plan := seeded(t, db, file)
	if !plan.HasChanges() {
		t.Fatal("expected the adoption to count as something to do")
	}
	if len(plan.Updates) != 0 {
		t.Errorf("expected no column written, got %+v", plan.Updates)
	}

	applyAll(t, db, file)

	if len(db.updatedRows) != 0 {
		t.Errorf("expected the row left alone, got %+v", db.updatedRows)
	}
	if _, tracked := db.state.Row("alpha"); !tracked {
		t.Error("expected it tracked anyway")
	}
}

func TestAdoptionInsertsWhenThereIsNothingToClaim(t *testing.T) {
	db := newMockDBAdapter()
	db.uniqueKeys = map[string][][]string{"fields": {{"xid"}}}

	file := entityFile("a.yaml", col("fields", "alpha", map[string]any{
		"xid":   "field-two",
		"label": "New",
	}))

	plan := seeded(t, db, file)

	if len(plan.Adopted) != 0 {
		t.Errorf("expected nothing adopted on an empty table, got %+v", plan.Adopted)
	}
	if len(plan.Inserts) != 1 {
		t.Errorf("expected an ordinary insert, got %+v", plan.Inserts)
	}
}

func TestAdoptionSkipsAKeyItCannotEvaluate(t *testing.T) {
	// A template resolves to something joka cannot predict, or to a primary key
	// that may not exist yet, so a key with one in it cannot identify an
	// existing row. The entity inserts rather than matching on a guess.
	db := seededDB(t)
	db.uniqueKeys = map[string][][]string{"fields": {{"label"}}}

	file := entityFile("a.yaml", col("fields", "alpha", map[string]any{
		"xid":   "field-one",
		"label": "{{ now }}",
	}))

	plan := seeded(t, db, file)

	if len(plan.Adopted) != 0 {
		t.Errorf("expected no match on a templated key, got %+v", plan.Adopted)
	}
	if len(plan.Inserts) != 1 {
		t.Errorf("expected it to fall through to an insert, got %+v", plan.Inserts)
	}
}

func TestAdoptedParentResolvesAReferenceToIt(t *testing.T) {
	// Every parent-child seed on a database joka is claiming for the first time
	// goes through this. Without the adopted primary key in the reference map
	// the plan dies with "not found in reference map".
	db := newMockDBAdapter()
	db.uniqueKeys = map[string][][]string{
		"parents":  {{"xid"}},
		"children": {{"xid"}},
	}
	db.currentRows["parents|4"] = map[string]any{"xid": "p1", "label": "Parent"}
	db.currentRows["children|9"] = map[string]any{"xid": "c1", "parent_id": int64(4)}

	file := entityFile("a.yaml",
		col("parents", "parent", map[string]any{"xid": "p1", "label": "Parent"},
			col("children", "child", map[string]any{
				"xid":       "c1",
				"parent_id": "{{ parent.id }}",
			}),
		),
	)

	plan := seeded(t, db, file)

	if len(plan.Adopted) != 2 {
		t.Fatalf("expected both rows adopted, got %+v", plan.Adopted)
	}
	if plan.Adopted["child"].Row.PKValue != 9 {
		t.Errorf("expected the child matched on its own key, got %+v", plan.Adopted["child"])
	}
}

func TestDecayedRewritesEveryColumnAndReportsNoConflict(t *testing.T) {
	db := newMockDBAdapter()

	file := entityFile("a.yaml", col("fields", "alpha", map[string]any{
		"label": "Alpha",
		"code":  "A",
	}))
	applyAll(t, db, file)

	// Both columns moved in the database. One of them would be a conflict,
	// and under decay neither is: being wrong is the premise of the sweep.
	db.currentRows["fields|1"] = map[string]any{"label": "Drifted", "code": "A"}

	plan, err := (PlanSyncAction{
		DB: db, State: db.state, Declared: []*domain.EntityFile{file}, Decayed: true,
	}).Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	if len(plan.Conflicts) != 0 {
		t.Errorf("expected no conflicts under decay, got %+v", plan.Conflicts)
	}

	written := plan.ColumnsToWrite()["alpha"]
	if len(written) != 2 {
		t.Errorf("expected every declared column written, got %v", written)
	}
}

func TestDecayedStillHonoursOnce(t *testing.T) {
	// _once names a column the application owns after seeding. A stale-seed
	// sweep is not a reason to reset every password.
	db := newMockDBAdapter()

	entity := col("users", "admin", map[string]any{
		"email":         "admin@example.com",
		"password_hash": "seeded",
	})
	entity.Once = []string{"password_hash"}
	file := entityFile("a.yaml", entity)
	applyAll(t, db, file)

	db.currentRows["users|1"] = map[string]any{
		"email":         "admin@example.com",
		"password_hash": "the user reset it",
	}

	plan, err := (PlanSyncAction{
		DB: db, State: db.state, Declared: []*domain.EntityFile{file}, Decayed: true,
	}).Execute(context.Background())
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for _, column := range plan.ColumnsToWrite()["admin"] {
		if column == "password_hash" {
			t.Error("expected the _once column left alone even under decay")
		}
	}
}

func TestAdoptionRecordsWhatTheRowHeld(t *testing.T) {
	// Without this an adopted entity has no baseline, so drift on it is
	// invisible: live-versus-baseline cannot be asked, every difference reads as
	// push, and the next hand-edit is silently overwritten. Adopting tic_main
	// left five of its six entities in exactly that state.
	db := seededDB(t)

	file := entityFile("a.yaml", col("fields", "alpha", map[string]any{
		"xid":   "field-one",
		"label": "Set up by hand", // already agrees
	}))

	applyAll(t, db, file)

	alpha, tracked := db.state.Row("alpha")
	if !tracked {
		t.Fatal("expected the row claimed")
	}
	if hash, ok := alpha.Baseline("label"); !ok || hash != HashValue("Set up by hand") {
		t.Fatalf("expected the live value recorded as the baseline, got %q ok=%v", hash, ok)
	}

	// And it does its job: the database moving that column is now a conflict
	// rather than something joka assumes it is free to overwrite.
	db.currentRows["fields|7"] = map[string]any{"xid": "field-one", "label": "Edited in the app"}

	plan := seeded(t, db, file)
	if len(plan.Conflicts) != 1 || plan.Conflicts[0].Columns[0].Column != "label" {
		t.Errorf("expected drift on the adopted row reported as a conflict, got %+v", plan.Conflicts)
	}
}

func TestUniqueKeysAreReadOncePerTable(t *testing.T) {
	// Measured at jjc2's shape — 294 entities over 18 tables — asking per
	// entity was 294 catalog queries where 18 would do, 23% of every round trip
	// a first adoption made.
	db := newMockDBAdapter()
	db.uniqueKeys = map[string][][]string{"fields": {{"xid"}}}

	entities := make([]domain.Entity, 0, 20)
	for i := 0; i < 20; i++ {
		entities = append(entities, col("fields", fmt.Sprintf("e%02d", i),
			map[string]any{"xid": fmt.Sprintf("x%02d", i)}))
	}

	seeded(t, db, entityFile("a.yaml", entities...))

	if db.uniqueKeyCalls != 1 {
		t.Errorf("expected one catalog read for one table across 20 entities, got %d",
			db.uniqueKeyCalls)
	}
}
