package infra_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/apsdsm/joka/internal/domains/migration/domain"
	"github.com/apsdsm/joka/internal/domains/migration/infra"
	"github.com/apsdsm/joka/testlib"
)

func TestPostgresCreateMigrationsTable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Cleanup(func() { testlib.DropTablePostgres(t, db, "joka_migrations") })

	adapter := infra.NewPostgresDBAdapter(db)
	ctx := context.Background()

	t.Run("it creates the table and confirms it exists", func(t *testing.T) {
		exists, err := adapter.HasMigrationsTable(ctx)
		if err != nil {
			t.Fatalf("HasMigrationsTable: %v", err)
		}
		if exists {
			t.Fatal("expected migrations table to not exist before creation")
		}

		if err := adapter.CreateMigrationsTable(ctx); err != nil {
			t.Fatalf("CreateMigrationsTable: %v", err)
		}

		exists, err = adapter.HasMigrationsTable(ctx)
		if err != nil {
			t.Fatalf("HasMigrationsTable after create: %v", err)
		}
		if !exists {
			t.Fatal("expected migrations table to exist after creation")
		}
	})

	t.Run("it returns ErrMigrationAlreadyExists when table exists", func(t *testing.T) {
		err := adapter.CreateMigrationsTable(ctx)
		if err == nil {
			t.Fatal("expected error on second CreateMigrationsTable, got nil")
		}
		if err != domain.ErrMigrationAlreadyExists {
			t.Fatalf("expected ErrMigrationAlreadyExists, got: %v", err)
		}
	})
}

func TestPostgresRecordAndGetAppliedMigrations(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Cleanup(func() { testlib.DropTablePostgres(t, db, "joka_migrations") })

	t.Run("it records and retrieves applied migrations in order", func(t *testing.T) {
		adapter := infra.NewPostgresDBAdapter(db)
		ctx := context.Background()

		if err := adapter.CreateMigrationsTable(ctx); err != nil {
			t.Fatalf("CreateMigrationsTable: %v", err)
		}

		if err := adapter.RecordMigrationApplied(ctx, "240101120000"); err != nil {
			t.Fatalf("RecordMigrationApplied #1: %v", err)
		}
		if err := adapter.RecordMigrationApplied(ctx, "240102120000"); err != nil {
			t.Fatalf("RecordMigrationApplied #2: %v", err)
		}

		rows, err := adapter.GetAppliedMigrations(ctx)
		if err != nil {
			t.Fatalf("GetAppliedMigrations: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("expected 2 rows, got %d", len(rows))
		}
		if rows[0].MigrationIndex != "240101120000" {
			t.Errorf("expected first index 240101120000, got %s", rows[0].MigrationIndex)
		}
		if rows[1].MigrationIndex != "240102120000" {
			t.Errorf("expected second index 240102120000, got %s", rows[1].MigrationIndex)
		}
	})
}

func TestPostgresApplySQLFromFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	adapter := infra.NewPostgresDBAdapter(db)
	ctx := context.Background()

	t.Run("it executes a single-statement SQL file", func(t *testing.T) {
		tableName := "test_pg_apply_sql"
		t.Cleanup(func() { testlib.DropTablePostgres(t, db, tableName) })

		dir := t.TempDir()
		sqlFile := filepath.Join(dir, "001_create.sql")
		content := "CREATE TABLE " + tableName + " (id INTEGER PRIMARY KEY, name VARCHAR(100));"
		if err := os.WriteFile(sqlFile, []byte(content), 0644); err != nil {
			t.Fatalf("writing sql file: %v", err)
		}

		if err := adapter.ApplySQLFromFile(ctx, sqlFile); err != nil {
			t.Fatalf("ApplySQLFromFile: %v", err)
		}

		_, err := db.ExecContext(ctx, "INSERT INTO "+tableName+" (id, name) VALUES (1, 'alice')")
		if err != nil {
			t.Fatalf("insert into created table: %v", err)
		}
	})

	t.Run("it executes a multi-statement SQL file", func(t *testing.T) {
		table1 := "test_pg_multi_a"
		table2 := "test_pg_multi_b"
		t.Cleanup(func() {
			testlib.DropTablePostgres(t, db, table1)
			testlib.DropTablePostgres(t, db, table2)
		})

		dir := t.TempDir()
		sqlFile := filepath.Join(dir, "002_multi.sql")
		content := "CREATE TABLE " + table1 + " (id INTEGER PRIMARY KEY);\nCREATE TABLE " + table2 + " (id INTEGER PRIMARY KEY);"
		if err := os.WriteFile(sqlFile, []byte(content), 0644); err != nil {
			t.Fatalf("writing sql file: %v", err)
		}

		if err := adapter.ApplySQLFromFile(ctx, sqlFile); err != nil {
			t.Fatalf("ApplySQLFromFile multi-statement: %v", err)
		}

		for _, tbl := range []string{table1, table2} {
			_, err := db.ExecContext(ctx, "INSERT INTO "+tbl+" (id) VALUES (1)")
			if err != nil {
				t.Fatalf("insert into %s: %v", tbl, err)
			}
		}
	})
}

func TestPostgresEnsureSnapshotsTable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Cleanup(func() { testlib.DropTablePostgres(t, db, "joka_snapshots") })

	t.Run("it creates the table and is idempotent", func(t *testing.T) {
		adapter := infra.NewPostgresDBAdapter(db)
		ctx := context.Background()

		if err := adapter.EnsureSnapshotsTable(ctx); err != nil {
			t.Fatalf("first EnsureSnapshotsTable: %v", err)
		}

		if err := adapter.EnsureSnapshotsTable(ctx); err != nil {
			t.Fatalf("second EnsureSnapshotsTable: %v", err)
		}
	})
}

func TestPostgresCaptureAndGetSchemaSnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	userTable := "test_pg_snapshot_users"
	t.Cleanup(func() {
		testlib.DropTablePostgres(t, db, "joka_snapshots")
		testlib.DropTablePostgres(t, db, userTable)
	})

	t.Run("it captures and retrieves a schema snapshot containing user tables", func(t *testing.T) {
		ctx := context.Background()

		_, err = db.ExecContext(ctx, "CREATE TABLE "+userTable+" (id INTEGER PRIMARY KEY, email VARCHAR(255))")
		if err != nil {
			t.Fatalf("creating user table: %v", err)
		}

		adapter := infra.NewPostgresDBAdapter(db)

		if err := adapter.CaptureSchemaSnapshot(ctx, "240101120000"); err != nil {
			t.Fatalf("CaptureSchemaSnapshot: %v", err)
		}

		snapshot, err := adapter.GetSchemaSnapshot(ctx, "240101120000")
		if err != nil {
			t.Fatalf("GetSchemaSnapshot: %v", err)
		}

		var schema map[string]string
		if err := json.Unmarshal([]byte(snapshot), &schema); err != nil {
			t.Fatalf("unmarshaling snapshot: %v", err)
		}

		createStmt, ok := schema[userTable]
		if !ok {
			t.Fatalf("snapshot missing table %s, got keys: %v", userTable, pgKeys(schema))
		}
		if createStmt == "" {
			t.Fatal("expected non-empty CREATE TABLE statement")
		}
	})
}

func TestPostgresGetLatestSnapshotIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Cleanup(func() { testlib.DropTablePostgres(t, db, "joka_snapshots") })

	t.Run("it returns the most recent snapshot index", func(t *testing.T) {
		adapter := infra.NewPostgresDBAdapter(db)
		ctx := context.Background()

		if err := adapter.EnsureSnapshotsTable(ctx); err != nil {
			t.Fatalf("EnsureSnapshotsTable: %v", err)
		}

		_, err = db.ExecContext(ctx, "INSERT INTO joka_snapshots (migration_index, schema_snapshot) VALUES ($1, $2)", "240101120000", "{}")
		if err != nil {
			t.Fatalf("inserting snapshot 1: %v", err)
		}
		_, err = db.ExecContext(ctx, "INSERT INTO joka_snapshots (migration_index, schema_snapshot) VALUES ($1, $2)", "240202120000", "{}")
		if err != nil {
			t.Fatalf("inserting snapshot 2: %v", err)
		}

		latest, err := adapter.GetLatestSnapshotIndex(ctx)
		if err != nil {
			t.Fatalf("GetLatestSnapshotIndex: %v", err)
		}
		if latest != "240202120000" {
			t.Errorf("expected latest snapshot 240202120000, got %s", latest)
		}
	})
}

func pgKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
