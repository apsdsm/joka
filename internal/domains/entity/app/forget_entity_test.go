package app

import (
	"context"
	"errors"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// trackedFile seeds the mock with a synced file and the rows it tracks.
func trackedFile(db *mockDBAdapter, path string, rows ...domain.TrackedRow) {
	db.track(path, rows...)
}

func TestForgetEntityAction(t *testing.T) {
	ctx := context.Background()

	t.Run("it reports each tracked row and whether it is still live", func(t *testing.T) {
		db := newMockDBAdapter()
		trackedFile(db, "a.yaml",
			domain.TrackedRow{TableName: "users", RowPK: 1, PKColumn: "id", RefID: "admin", InsertionOrder: 0},
			domain.TrackedRow{TableName: "profiles", RowPK: 2, PKColumn: "id", InsertionOrder: 1},
		)
		db.currentRows["users|1"] = map[string]any{"id": 1}

		plan, err := ForgetEntityAction{DB: db, State: db.state, FilePath: "a.yaml"}.Plan(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(plan.Rows) != 2 {
			t.Fatalf("expected 2 rows, got %d", len(plan.Rows))
		}
		if plan.Live != 1 {
			t.Errorf("expected 1 live row, got %d", plan.Live)
		}
		if !plan.Rows[0].Live || plan.Rows[0].RefID != "admin" {
			t.Errorf("expected the users row live and carrying its _id, got %+v", plan.Rows[0])
		}
		if plan.Rows[1].Live {
			t.Errorf("expected the profiles row gone, got %+v", plan.Rows[1])
		}
	})

	t.Run("it orders rows by insertion order", func(t *testing.T) {
		db := newMockDBAdapter()
		trackedFile(db, "a.yaml",
			domain.TrackedRow{TableName: "parents", RowPK: 1, PKColumn: "id", InsertionOrder: 0},
			domain.TrackedRow{TableName: "children", RowPK: 2, PKColumn: "id", InsertionOrder: 1},
		)

		plan, err := ForgetEntityAction{DB: db, State: db.state, FilePath: "a.yaml"}.Plan(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// The plan is for reading, so it runs parent-first even though deletion
		// has to run the other way.
		if plan.Rows[0].Table != "parents" || plan.Rows[1].Table != "children" {
			t.Errorf("expected parents before children, got %s then %s", plan.Rows[0].Table, plan.Rows[1].Table)
		}
	})

	t.Run("it marks a row whose table was dropped rather than querying it", func(t *testing.T) {
		db := newMockDBAdapter()
		db.missingTables = map[string]bool{"slot_assignments": true}
		trackedFile(db, "gone.yaml",
			domain.TrackedRow{TableName: "slot_assignments", RowPK: 5, PKColumn: "id", InsertionOrder: 0},
		)

		plan, err := ForgetEntityAction{DB: db, State: db.state, FilePath: "gone.yaml"}.Plan(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !plan.Rows[0].TableMissing {
			t.Error("expected the row marked as having no table")
		}
		if plan.Rows[0].Live || plan.Live != 0 {
			t.Error("expected a row in a dropped table not to count as live")
		}
	})

	t.Run("it removes both tracking records when nothing is live", func(t *testing.T) {
		db := newMockDBAdapter()
		trackedFile(db, "a.yaml",
			domain.TrackedRow{TableName: "users", RowPK: 1, PKColumn: "id", InsertionOrder: 0},
		)

		plan, err := ForgetEntityAction{DB: db, State: db.state, FilePath: "a.yaml"}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(plan.Rows) != 1 {
			t.Errorf("expected the plan to report the row it removed, got %d", len(plan.Rows))
		}
		if len(db.entityRows) != 0 {
			t.Errorf("expected joka_entity_rows cleared, got %d rows", len(db.entityRows))
		}
		if db.synced["a.yaml"] {
			t.Error("expected the joka_entities record removed")
		}
	})

	t.Run("it never deletes the rows themselves", func(t *testing.T) {
		db := newMockDBAdapter()
		trackedFile(db, "a.yaml",
			domain.TrackedRow{TableName: "users", RowPK: 1, PKColumn: "id", InsertionOrder: 0},
		)

		if _, err := (ForgetEntityAction{DB: db, State: db.state, FilePath: "a.yaml"}).Execute(ctx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(db.deletedRows) != 0 {
			t.Errorf("expected no row deletions, got %+v", db.deletedRows)
		}
	})

	t.Run("it refuses when a tracked row is still in the database", func(t *testing.T) {
		db := newMockDBAdapter()
		trackedFile(db, "a.yaml",
			domain.TrackedRow{TableName: "users", RowPK: 1, PKColumn: "id", InsertionOrder: 0},
		)
		db.currentRows["users|1"] = map[string]any{"id": 1}

		plan, err := ForgetEntityAction{DB: db, State: db.state, FilePath: "a.yaml"}.Execute(ctx)
		if !errors.Is(err, domain.ErrRowsStillLive) {
			t.Fatalf("expected ErrRowsStillLive, got %v", err)
		}
		if plan == nil {
			t.Fatal("expected the plan returned alongside the refusal so the caller can show the rows")
		}
		if len(db.entityRows) != 1 || !db.synced["a.yaml"] {
			t.Error("expected the refusal to leave the tracking untouched")
		}
	})

	t.Run("it forgets a live row when forced", func(t *testing.T) {
		db := newMockDBAdapter()
		trackedFile(db, "a.yaml",
			domain.TrackedRow{TableName: "users", RowPK: 1, PKColumn: "id", InsertionOrder: 0},
		)
		db.currentRows["users|1"] = map[string]any{"id": 1}

		plan, err := ForgetEntityAction{DB: db, State: db.state, FilePath: "a.yaml", Force: true}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if plan.Live != 1 {
			t.Errorf("expected the plan to still report the live row, got %d", plan.Live)
		}
		if db.synced["a.yaml"] {
			t.Error("expected the tracking removed")
		}
		if len(db.deletedRows) != 0 {
			t.Error("expected --force to forget the tracking, not delete the row")
		}
	})

	t.Run("it refuses a file that was never synced", func(t *testing.T) {
		db := newMockDBAdapter()

		_, err := ForgetEntityAction{DB: db, State: db.state, FilePath: "unknown.yaml"}.Execute(ctx)
		if !errors.Is(err, domain.ErrEntityNotSynced) {
			t.Fatalf("expected ErrEntityNotSynced, got %v", err)
		}
	})

	t.Run("it forgets a tracked file that has no tracked rows", func(t *testing.T) {
		// Files synced before row tracking existed have a joka_entities record
		// and nothing in joka_entity_rows.
		db := newMockDBAdapter()
		trackedFile(db, "a.yaml")

		plan, err := ForgetEntityAction{DB: db, State: db.state, FilePath: "a.yaml"}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(plan.Rows) != 0 {
			t.Errorf("expected no rows in the plan, got %d", len(plan.Rows))
		}
		if db.synced["a.yaml"] {
			t.Error("expected the joka_entities record removed")
		}
	})

	t.Run("it checks each table only once", func(t *testing.T) {
		db := &countingAdapter{mockDBAdapter: *newMockDBAdapter()}
		trackedFile(&db.mockDBAdapter, "a.yaml",
			domain.TrackedRow{TableName: "users", RowPK: 1, PKColumn: "id", InsertionOrder: 0},
			domain.TrackedRow{TableName: "users", RowPK: 2, PKColumn: "id", InsertionOrder: 1},
			domain.TrackedRow{TableName: "users", RowPK: 3, PKColumn: "id", InsertionOrder: 2},
		)

		if _, err := (ForgetEntityAction{DB: db, State: db.state, FilePath: "a.yaml"}).Plan(ctx); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if db.tableChecks != 1 {
			t.Errorf("expected 1 table check for 3 rows in the same table, got %d", db.tableChecks)
		}
	})
}

// countingAdapter counts TableExists calls to prove the plan caches them.
type countingAdapter struct {
	mockDBAdapter
	tableChecks int
}

func (c *countingAdapter) TableExists(ctx context.Context, table string) (bool, error) {
	c.tableChecks++
	return c.mockDBAdapter.TableExists(ctx, table)
}

// defaultPKColumn covers rows tracked before pk_column was recorded.
func TestForgetEntityActionDefaultsPKColumn(t *testing.T) {
	db := newMockDBAdapter()
	trackedFile(db, "a.yaml",
		domain.TrackedRow{TableName: "users", RowPK: 1, PKColumn: "", InsertionOrder: 0},
	)

	plan, err := ForgetEntityAction{DB: db, State: db.state, FilePath: "a.yaml"}.Plan(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if plan.Rows[0].PKColumn != "id" {
		t.Errorf("expected an empty pk_column to default to id, got %q", plan.Rows[0].PKColumn)
	}
}
