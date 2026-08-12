package infra_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/apsdsm/joka/internal/domains/migration/domain"
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
		t.Fatalf("ComputeSchema: %v", err)
	}

	if _, ok := schema["test_my_view_v"]; ok {
		t.Errorf("expected the view to be excluded from the table snapshot, got keys: %v", keys(schema))
	}
	if _, ok := schema["test_my_view_base"]; !ok {
		t.Errorf("expected the base table in the snapshot, got keys: %v", keys(schema))
	}
}

func TestMySQLUnsupportedSchemaObjects(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}
	ctx := context.Background()
	adapter := infra.NewMySQLDBAdapter(db)

	t.Run("it reports nothing for a schema of plain tables", func(t *testing.T) {
		t.Cleanup(func() { testlib.DropTable(t, db, "test_my_unsup_plain") })
		execMySQL(t, db, "CREATE TABLE test_my_unsup_plain (id INT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(50))")

		objects, err := adapter.UnsupportedSchemaObjects(ctx)
		if err != nil {
			t.Fatalf("UnsupportedSchemaObjects: %v", err)
		}
		if len(objects) != 0 {
			t.Errorf("expected no unsupported objects, got %v", objects)
		}
	})

	t.Run("it reports views, routines and triggers", func(t *testing.T) {
		t.Cleanup(func() {
			execMySQL(t, db, "DROP VIEW IF EXISTS test_my_unsup_view")
			execMySQL(t, db, "DROP TRIGGER IF EXISTS test_my_unsup_trg")
			execMySQL(t, db, "DROP PROCEDURE IF EXISTS test_my_unsup_proc")
			testlib.DropTable(t, db, "test_my_unsup_base")
		})

		execMySQL(t, db, "CREATE TABLE test_my_unsup_base (id INT PRIMARY KEY, n INT)")
		execMySQL(t, db, "CREATE VIEW test_my_unsup_view AS SELECT id FROM test_my_unsup_base")
		execMySQL(t, db, "CREATE PROCEDURE test_my_unsup_proc() SELECT 1")
		execMySQL(t, db, "CREATE TRIGGER test_my_unsup_trg BEFORE INSERT ON test_my_unsup_base FOR EACH ROW SET NEW.n = 1")

		objects, err := adapter.UnsupportedSchemaObjects(ctx)
		if err != nil {
			t.Fatalf("UnsupportedSchemaObjects: %v", err)
		}

		joined := strings.Join(objects, "\n")
		for _, want := range []string{
			"view test_my_unsup_view",
			"procedure test_my_unsup_proc",
			"trigger test_my_unsup_trg",
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("expected %q to be reported, got:\n%s", want, joined)
			}
		}
	})
}

func TestMySQLValidateSchemaSQLIsUnsupported(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	adapter := infra.NewMySQLDBAdapter(db)
	err = adapter.ValidateSchemaSQL(context.Background(), "CREATE TABLE whatever (id INT);")
	if !errors.Is(err, domain.ErrSchemaValidationUnsupported) {
		t.Fatalf("expected ErrSchemaValidationUnsupported, got: %v", err)
	}
}

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
