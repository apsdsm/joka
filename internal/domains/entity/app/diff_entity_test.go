package app

import (
	"context"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// entity builds a declared entity with an optional child chain.
func entity(table, refID string, cols map[string]any, children ...domain.Entity) domain.Entity {
	if cols == nil {
		cols = map[string]any{}
	}
	return domain.Entity{Table: table, RefID: refID, PKColumn: "id", Columns: cols, Children: children}
}

// row builds a tracked row.
func row(table, refID string, pk int64, order int) domain.TrackedRow {
	return domain.TrackedRow{TableName: table, RefID: refID, RowPK: pk, PKColumn: "id", InsertionOrder: order}
}

// diffFixture seeds the mock with a synced file and its tracked rows, and
// marks every row live.
func diffFixture(db *mockDBAdapter, path string, rows ...domain.TrackedRow) {
	db.synced[path] = true
	db.entityHashes[path] = "hash"
	for _, r := range rows {
		r.EntityFile = path
		db.entityRows = append(db.entityRows, r)
		db.currentRows[r.TableName+"|"+itoa(r.RowPK)] = map[string]any{}
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var out []byte
	for n > 0 {
		out = append([]byte{byte('0' + n%10)}, out...)
		n /= 10
	}
	return string(out)
}

func lineAt(t *testing.T, d *EntityDiff, i int) DiffLine {
	t.Helper()
	if i >= len(d.Lines) {
		t.Fatalf("expected at least %d lines, got %d", i+1, len(d.Lines))
	}
	return d.Lines[i]
}

func TestDiffEntityAction(t *testing.T) {
	ctx := context.Background()

	t.Run("it matches by _id when both sides are fully keyed", func(t *testing.T) {
		db := newMockDBAdapter()
		diffFixture(db, "a.yaml",
			row("users", "admin", 1, 0),
			row("profiles", "admin_profile", 2, 1),
		)

		d, err := DiffEntityAction{DB: db, Path: "a.yaml", OnDisk: true, SkipValues: true, Entities: []domain.Entity{
			entity("users", "admin", nil, entity("profiles", "admin_profile", nil)),
		}}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if d.MatchedBy != MatchByID {
			t.Errorf("expected an _id match, got %q", d.MatchedBy)
		}
		if !d.KeyedByID {
			t.Error("expected keyed_by_id")
		}
		if d.Inserts != 0 || d.Deletes != 0 {
			t.Errorf("expected nothing to insert or delete, got %d/%d", d.Inserts, d.Deletes)
		}
	})

	t.Run("it places an entity added mid-file as an insert without shifting the rest", func(t *testing.T) {
		// The failure this command exists for: sync reports "3 entities but 2
		// are tracked" and recommends a destructive reimport.
		db := newMockDBAdapter()
		diffFixture(db, "a.yaml",
			row("fields", "first", 1, 0),
			row("fields", "third", 2, 1),
		)

		d, err := DiffEntityAction{DB: db, Path: "a.yaml", OnDisk: true, SkipValues: true, Entities: []domain.Entity{
			entity("fields", "first", nil),
			entity("fields", "second", nil),
			entity("fields", "third", nil),
		}}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if d.Inserts != 1 || d.Deletes != 0 {
			t.Fatalf("expected exactly 1 insert and no deletes, got %d/%d", d.Inserts, d.Deletes)
		}
		if got := lineAt(t, d, 1); got.Status != DiffInsert || got.RefID != "second" {
			t.Errorf("expected the new entity reported as an insert, got %+v", got)
		}
		// The third entity is the same row, further down the file.
		if got := lineAt(t, d, 2); got.Status != DiffSame || !got.Moved || got.PKValue != 2 {
			t.Errorf("expected the third entity matched to its own row and marked moved, got %+v", got)
		}
		if d.PositionalBreak != 2 {
			t.Errorf("expected positional alignment to break at #2, got %d", d.PositionalBreak)
		}
		if !d.KeyedByID {
			t.Error("expected keyed_by_id, which is what makes the _id match exact here")
		}
	})

	t.Run("it reports an entity removed from the file as a delete", func(t *testing.T) {
		db := newMockDBAdapter()
		diffFixture(db, "a.yaml",
			row("fields", "first", 1, 0),
			row("fields", "second", 2, 1),
		)

		d, err := DiffEntityAction{DB: db, Path: "a.yaml", OnDisk: true, SkipValues: true, Entities: []domain.Entity{
			entity("fields", "first", nil),
		}}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if d.Deletes != 1 {
			t.Fatalf("expected 1 delete, got %d", d.Deletes)
		}
		if got := lineAt(t, d, 1); got.Status != DiffDelete || got.RefID != "second" {
			t.Errorf("expected the dropped entity reported as a delete, got %+v", got)
		}
	})

	t.Run("it falls back to positional matching when an _id is missing", func(t *testing.T) {
		db := newMockDBAdapter()
		diffFixture(db, "a.yaml", row("fields", "", 1, 0))

		d, err := DiffEntityAction{DB: db, Path: "a.yaml", OnDisk: true, SkipValues: true, Entities: []domain.Entity{
			entity("fields", "", nil),
		}}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if d.MatchedBy != MatchByPosition {
			t.Errorf("expected a positional match, got %q", d.MatchedBy)
		}
		if d.KeyedByID {
			t.Error("expected keyed_by_id false")
		}
		if len(d.UnkeyedDeclared) != 1 || len(d.UnkeyedTracked) != 1 {
			t.Errorf("expected both sides named as unkeyed, got %v / %v", d.UnkeyedDeclared, d.UnkeyedTracked)
		}
	})

	t.Run("it flags a positional pairing that lands in a different table", func(t *testing.T) {
		db := newMockDBAdapter()
		diffFixture(db, "a.yaml", row("profiles", "", 1, 0))

		d, err := DiffEntityAction{DB: db, Path: "a.yaml", OnDisk: true, SkipValues: true, Entities: []domain.Entity{
			entity("users", "", nil),
		}}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		got := lineAt(t, d, 0)
		if got.Status != DiffUnpaired {
			t.Errorf("expected the mismatched tables flagged, got %q", got.Status)
		}
		if got.DeclaredTable != "users" || got.Table != "profiles" {
			t.Errorf("expected both table names reported, got %+v", got)
		}
	})

	t.Run("it reports a tracked row that is no longer in the database", func(t *testing.T) {
		db := newMockDBAdapter()
		db.synced["a.yaml"] = true
		db.entityRows = append(db.entityRows, domain.TrackedRow{
			EntityFile: "a.yaml", TableName: "users", RefID: "admin", RowPK: 1, PKColumn: "id",
		})
		// currentRows is left empty, so the row reads as gone.

		d, err := DiffEntityAction{DB: db, Path: "a.yaml", OnDisk: true, SkipValues: true, Entities: []domain.Entity{
			entity("users", "admin", nil),
		}}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if d.MissingRows != 1 {
			t.Errorf("expected 1 missing row, got %d", d.MissingRows)
		}
		if got := lineAt(t, d, 0); got.Live {
			t.Error("expected the row reported as not live")
		}
	})

	t.Run("it marks a row whose table was dropped", func(t *testing.T) {
		db := newMockDBAdapter()
		db.missingTables = map[string]bool{"slots": true}
		db.synced["a.yaml"] = true
		db.entityRows = append(db.entityRows, domain.TrackedRow{
			EntityFile: "a.yaml", TableName: "slots", RowPK: 5, PKColumn: "id",
		})

		d, err := DiffEntityAction{DB: db, Path: "a.yaml", OnDisk: false, SkipValues: true}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got := lineAt(t, d, 0); !got.TableMissing {
			t.Errorf("expected the dropped table flagged, got %+v", got)
		}
	})

	t.Run("it treats an untracked file as all inserts", func(t *testing.T) {
		db := newMockDBAdapter()

		d, err := DiffEntityAction{DB: db, Path: "new.yaml", OnDisk: true, SkipValues: true, Entities: []domain.Entity{
			entity("users", "admin", nil),
			entity("users", "other", nil),
		}}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if d.Tracked {
			t.Error("expected tracked false")
		}
		if d.Inserts != 2 {
			t.Errorf("expected 2 inserts, got %d", d.Inserts)
		}
		// There is nothing on the tracked side, so there is no alignment to
		// have broken.
		if d.PositionalBreak != 0 {
			t.Errorf("expected no positional break on an untracked file, got %d", d.PositionalBreak)
		}
	})

	t.Run("it treats an orphan as all deletes", func(t *testing.T) {
		db := newMockDBAdapter()
		diffFixture(db, "gone.yaml", row("users", "admin", 1, 0))

		d, err := DiffEntityAction{DB: db, Path: "gone.yaml", OnDisk: false, SkipValues: true}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if d.OnDisk {
			t.Error("expected on_disk false")
		}
		if d.Deletes != 1 {
			t.Errorf("expected 1 delete, got %d", d.Deletes)
		}
		if d.SyncVerdict != "" {
			t.Error("expected no sync verdict for a file sync would never look at")
		}
	})

	t.Run("it reports the columns that changed on a matched row", func(t *testing.T) {
		db := newMockDBAdapter()
		diffFixture(db, "a.yaml", row("fields", "first", 1, 0))
		db.currentRows["fields|1"] = map[string]any{"label": "old"}

		d, err := DiffEntityAction{DB: db, Path: "a.yaml", OnDisk: true, Entities: []domain.Entity{
			entity("fields", "first", map[string]any{"label": "new"}),
		}}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		line := lineAt(t, d, 0)
		if line.Status != DiffChanged {
			t.Errorf("expected the line marked changed, got %q", line.Status)
		}
		if len(line.Changes) != 1 || line.Changes[0].Column != "label" {
			t.Fatalf("expected the label change reported, got %+v", line.Changes)
		}
		if line.Changes[0].Before != "old" || line.Changes[0].After != "new" {
			t.Errorf("expected old → new, got %q → %q", line.Changes[0].Before, line.Changes[0].After)
		}
		if d.Changes != 1 {
			t.Errorf("expected 1 changed row, got %d", d.Changes)
		}
	})

	t.Run("it skips the value comparison when asked", func(t *testing.T) {
		db := newMockDBAdapter()
		diffFixture(db, "a.yaml", row("fields", "first", 1, 0))
		db.currentRows["fields|1"] = map[string]any{"label": "old"}

		d, err := DiffEntityAction{DB: db, Path: "a.yaml", OnDisk: true, SkipValues: true, Entities: []domain.Entity{
			entity("fields", "first", map[string]any{"label": "new"}),
		}}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(lineAt(t, d, 0).Changes) != 0 {
			t.Error("expected no column comparison")
		}
		if lineAt(t, d, 0).ChangesSkipped == "" {
			t.Error("expected the skip explained rather than silently empty")
		}
	})

	t.Run("it does not compare values on a row that is not in the database", func(t *testing.T) {
		db := newMockDBAdapter()
		db.synced["a.yaml"] = true
		db.entityRows = append(db.entityRows, domain.TrackedRow{
			EntityFile: "a.yaml", TableName: "fields", RefID: "first", RowPK: 1, PKColumn: "id",
		})

		d, err := DiffEntityAction{DB: db, Path: "a.yaml", OnDisk: true, Entities: []domain.Entity{
			entity("fields", "first", map[string]any{"label": "new"}),
		}}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		line := lineAt(t, d, 0)
		if len(line.Changes) != 0 {
			t.Errorf("expected no comparison against a row that is gone, got %+v", line.Changes)
		}
		if line.ChangesSkipped != "row is not in the database" {
			t.Errorf("expected the reason recorded, got %q", line.ChangesSkipped)
		}
	})
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}

func TestDiffEntityRegeneratedColumns(t *testing.T) {
	ctx := context.Background()

	t.Run("it does not mark a row changed for a regenerated column alone", func(t *testing.T) {
		// A file with created_at: "{{ now }}" would otherwise report every one
		// of its rows as changed, forever.
		db := newMockDBAdapter()
		diffFixture(db, "a.yaml", row("fields", "first", 1, 0), row("fields", "second", 2, 1))
		db.currentRows["fields|1"] = map[string]any{"created_at": "2020-01-01 00:00:00", "label": "A"}
		db.currentRows["fields|2"] = map[string]any{"created_at": "2020-01-01 00:00:00", "label": "B"}

		d, err := DiffEntityAction{DB: db, Path: "a.yaml", OnDisk: true, Entities: []domain.Entity{
			entity("fields", "first", map[string]any{"created_at": "{{ now }}", "label": "A"}),
			entity("fields", "second", map[string]any{"created_at": "{{ now }}", "label": "B"}),
		}}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if d.Changes != 0 {
			t.Errorf("expected no changed rows, got %d", d.Changes)
		}
		for i, line := range d.Lines {
			if line.Status != DiffSame {
				t.Errorf("line %d: expected same, got %q", i+1, line.Status)
			}
			if len(line.Changes) != 0 {
				t.Errorf("line %d: expected the regenerated column off the row, got %+v", i+1, line.Changes)
			}
		}
	})

	t.Run("it collects the regenerated columns once for the file", func(t *testing.T) {
		db := newMockDBAdapter()
		diffFixture(db, "a.yaml", row("fields", "first", 1, 0), row("fields", "second", 2, 1))
		db.currentRows["fields|1"] = map[string]any{"created_at": "x", "secret": "y"}
		db.currentRows["fields|2"] = map[string]any{"created_at": "x", "secret": "y"}

		d, err := DiffEntityAction{DB: db, Path: "a.yaml", OnDisk: true, Entities: []domain.Entity{
			entity("fields", "first", map[string]any{"created_at": "{{ now }}", "secret": "{{ argon2id|pw }}"}),
			entity("fields", "second", map[string]any{"created_at": "{{ now }}", "secret": "{{ argon2id|pw }}"}),
		}}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(d.RegeneratedColumns) != 2 {
			t.Fatalf("expected 2 regenerated columns deduplicated across rows, got %v", d.RegeneratedColumns)
		}
		if d.RegeneratedColumns[0] != "created_at" || d.RegeneratedColumns[1] != "secret" {
			t.Errorf("expected them sorted, got %v", d.RegeneratedColumns)
		}
	})

	t.Run("it still reports a real change alongside a regenerated column", func(t *testing.T) {
		db := newMockDBAdapter()
		diffFixture(db, "a.yaml", row("fields", "first", 1, 0))
		db.currentRows["fields|1"] = map[string]any{"created_at": "x", "label": "old"}

		d, err := DiffEntityAction{DB: db, Path: "a.yaml", OnDisk: true, Entities: []domain.Entity{
			entity("fields", "first", map[string]any{"created_at": "{{ now }}", "label": "new"}),
		}}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if d.Changes != 1 {
			t.Fatalf("expected the real change counted, got %d", d.Changes)
		}
		line := lineAt(t, d, 0)
		if len(line.Changes) != 1 || line.Changes[0].Column != "label" {
			t.Errorf("expected only the label change on the row, got %+v", line.Changes)
		}
	})

	t.Run("it does not report a JSON column whose keys are merely ordered differently", func(t *testing.T) {
		db := newMockDBAdapter()
		diffFixture(db, "a.yaml", row("fields", "first", 1, 0))
		db.currentRows["fields|1"] = map[string]any{"label": `{"en": "Capital", "ja": "資本金"}`}

		d, err := DiffEntityAction{DB: db, Path: "a.yaml", OnDisk: true, Entities: []domain.Entity{
			entity("fields", "first", map[string]any{"label": `{"ja":"資本金","en":"Capital"}`}),
		}}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if d.Changes != 0 {
			t.Errorf("expected no change for a reordered JSON object, got %+v", d.Lines[0].Changes)
		}
	})
}

func TestDiffEntityDepth(t *testing.T) {
	ctx := context.Background()

	t.Run("it records the nesting depth of each declared entity", func(t *testing.T) {
		db := newMockDBAdapter()
		diffFixture(db, "a.yaml",
			row("fields", "f", 1, 0),
			row("field_versions", "f_v1", 2, 1),
			row("notes", "f_note", 3, 2),
			row("fields", "g", 4, 3),
		)

		d, err := DiffEntityAction{DB: db, Path: "a.yaml", OnDisk: true, SkipValues: true, Entities: []domain.Entity{
			entity("fields", "f", nil,
				entity("field_versions", "f_v1", nil,
					entity("notes", "f_note", nil),
				),
			),
			entity("fields", "g", nil),
		}}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Depth-first pre-order, which is also the insertion order.
		for i, want := range []int{0, 1, 2, 0} {
			if got := lineAt(t, d, i).Depth; got != want {
				t.Errorf("line %d: expected depth %d, got %d", i+1, want, got)
			}
		}
	})

	t.Run("it leaves a line with no declared side at depth zero", func(t *testing.T) {
		// A tracked row records no nesting, so a delete has none to report.
		db := newMockDBAdapter()
		diffFixture(db, "a.yaml", row("fields", "gone", 1, 0))

		d, err := DiffEntityAction{DB: db, Path: "a.yaml", OnDisk: true, SkipValues: true}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		line := lineAt(t, d, 0)
		if line.Status != DiffDelete {
			t.Fatalf("expected a delete, got %q", line.Status)
		}
		if line.Depth != 0 {
			t.Errorf("expected depth 0, got %d", line.Depth)
		}
	})

	t.Run("its depths line up with the flattened order sync uses", func(t *testing.T) {
		// flattenDepths must walk the graph exactly as flattenEntities does, or
		// every depth would be attached to the wrong row.
		entities := []domain.Entity{
			entity("a", "a", nil, entity("b", "b", nil), entity("c", "c", nil, entity("d", "d", nil))),
			entity("e", "e", nil),
		}

		flat := flattenEntities(entities, nil)
		depths := flattenDepths(entities, 0, nil)

		if len(flat) != len(depths) {
			t.Fatalf("expected one depth per entity, got %d and %d", len(flat), len(depths))
		}
		for i, want := range []int{0, 1, 1, 2, 0} {
			if depths[i] != want {
				t.Errorf("%s: expected depth %d, got %d", flat[i].Table, want, depths[i])
			}
		}
	})
}
