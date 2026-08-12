package infra_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/apsdsm/joka/internal/domains/migration/infra"
	"github.com/apsdsm/joka/testlib"
)

// execMySQL runs a statement and fails the test on error.
func execMySQL(t *testing.T, db *sql.DB, stmt string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), stmt); err != nil {
		t.Fatalf("exec %q: %v", stmt, err)
	}
}

func TestMySQLComputeSchemaSkipsViews(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}
	ctx := context.Background()

	t.Cleanup(func() {
		execMySQL(t, db, "DROP VIEW IF EXISTS test_my_view_v")
		testlib.DropTable(t, db, "test_my_view_base")
	})

	execMySQL(t, db, "CREATE TABLE test_my_view_base (id INT PRIMARY KEY, name VARCHAR(50))")
	execMySQL(t, db, "CREATE VIEW test_my_view_v AS SELECT id FROM test_my_view_base")

	adapter := infra.NewMySQLDBAdapter(db)
	schema, err := adapter.ComputeSchema(ctx)
	if err != nil {
		// Before the BASE TABLE filter this failed outright: SHOW CREATE TABLE on
		// a view returns four columns, not two.
		t.Fatalf("ComputeSchema: %v", err)
	}

	if _, ok := schema["test_my_view_v"]; ok {
		t.Errorf("expected the view to be excluded from the table snapshot, got keys: %v", keys(schema))
	}
	if _, ok := schema["test_my_view_base"]; !ok {
		t.Errorf("expected the base table in the snapshot, got keys: %v", keys(schema))
	}
}

// TestMySQLRemoveMigrationRecords covers the bookkeeping half of consolidate.
func TestMySQLRemoveMigrationRecords(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}
	ctx := context.Background()

	t.Cleanup(func() {
		testlib.DropTable(t, db, "joka_migrations")
		testlib.DropTable(t, db, "joka_snapshots")
	})

	adapter := infra.NewMySQLDBAdapter(db)
	if err := adapter.CreateMigrationsTable(ctx); err != nil {
		t.Fatalf("CreateMigrationsTable: %v", err)
	}

	for _, index := range []string{"240101000000", "240102000000", "240103000000"} {
		if err := adapter.RecordMigrationApplied(ctx, index); err != nil {
			t.Fatalf("RecordMigrationApplied: %v", err)
		}
		if err := adapter.CaptureSchemaSnapshot(ctx, index); err != nil {
			t.Fatalf("CaptureSchemaSnapshot: %v", err)
		}
	}

	if err := adapter.RemoveMigrationRecords(ctx, []string{"240101000000", "240102000000"}); err != nil {
		t.Fatalf("RemoveMigrationRecords: %v", err)
	}

	applied, err := adapter.GetAppliedMigrations(ctx)
	if err != nil {
		t.Fatalf("GetAppliedMigrations: %v", err)
	}
	if len(applied) != 1 || applied[0].MigrationIndex != "240103000000" {
		t.Fatalf("expected only 240103000000 to remain, got %+v", applied)
	}

	if _, err := adapter.GetSchemaSnapshot(ctx, "240101000000"); err == nil {
		t.Error("expected the snapshot for a removed migration to be gone")
	}
	if _, err := adapter.GetSchemaSnapshot(ctx, "240103000000"); err != nil {
		t.Errorf("expected the retained migration's snapshot to survive: %v", err)
	}
}
