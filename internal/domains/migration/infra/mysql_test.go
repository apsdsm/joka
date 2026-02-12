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

func TestCreateMigrationsTable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Cleanup(func() { testlib.DropTable(t, db, "joka_migrations") })

	adapter := infra.NewMySQLDBAdapter(db)
	ctx := context.Background()

	// Should not exist yet.
	exists, err := adapter.HasMigrationsTable(ctx)
	if err != nil {
		t.Fatalf("HasMigrationsTable: %v", err)
	}
	if exists {
		t.Fatal("expected migrations table to not exist before creation")
	}

	// Create it.
	if err := adapter.CreateMigrationsTable(ctx); err != nil {
		t.Fatalf("CreateMigrationsTable: %v", err)
	}

	// Should exist now.
	exists, err = adapter.HasMigrationsTable(ctx)
	if err != nil {
		t.Fatalf("HasMigrationsTable after create: %v", err)
	}
	if !exists {
		t.Fatal("expected migrations table to exist after creation")
	}
}

func TestCreateMigrationsTable_AlreadyExists(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Cleanup(func() { testlib.DropTable(t, db, "joka_migrations") })

	adapter := infra.NewMySQLDBAdapter(db)
	ctx := context.Background()

	if err := adapter.CreateMigrationsTable(ctx); err != nil {
		t.Fatalf("first CreateMigrationsTable: %v", err)
	}

	err = adapter.CreateMigrationsTable(ctx)
	if err == nil {
		t.Fatal("expected error on second CreateMigrationsTable, got nil")
	}
	if err != domain.ErrMigrationAlreadyExists {
		t.Fatalf("expected ErrMigrationAlreadyExists, got: %v", err)
	}
}

func TestRecordAndGetAppliedMigrations(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Cleanup(func() { testlib.DropTable(t, db, "joka_migrations") })

	adapter := infra.NewMySQLDBAdapter(db)
	ctx := context.Background()

	if err := adapter.CreateMigrationsTable(ctx); err != nil {
		t.Fatalf("CreateMigrationsTable: %v", err)
	}

	// Record two migrations.
	if err := adapter.RecordMigrationApplied(ctx, "240101120000"); err != nil {
		t.Fatalf("RecordMigrationApplied #1: %v", err)
	}
	if err := adapter.RecordMigrationApplied(ctx, "240102120000"); err != nil {
		t.Fatalf("RecordMigrationApplied #2: %v", err)
	}

	// Read them back.
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
}

func TestApplySQLFromFile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	tableName := "test_apply_sql"
	t.Cleanup(func() { testlib.DropTable(t, db, tableName) })

	// Write a temporary SQL file.
	dir := t.TempDir()
	sqlFile := filepath.Join(dir, "001_create.sql")
	content := "CREATE TABLE " + tableName + " (id INT PRIMARY KEY, name VARCHAR(100));"
	if err := os.WriteFile(sqlFile, []byte(content), 0644); err != nil {
		t.Fatalf("writing sql file: %v", err)
	}

	adapter := infra.NewMySQLDBAdapter(db)
	ctx := context.Background()

	if err := adapter.ApplySQLFromFile(ctx, sqlFile); err != nil {
		t.Fatalf("ApplySQLFromFile: %v", err)
	}

	// Verify the table was created by inserting a row.
	_, err = db.ExecContext(ctx, "INSERT INTO "+tableName+" (id, name) VALUES (1, 'alice')")
	if err != nil {
		t.Fatalf("insert into created table: %v", err)
	}
}

func TestApplySQLFromFile_MultiStatement(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	table1 := "test_multi_a"
	table2 := "test_multi_b"
	t.Cleanup(func() {
		testlib.DropTable(t, db, table1)
		testlib.DropTable(t, db, table2)
	})

	// Write a SQL file with two statements.
	dir := t.TempDir()
	sqlFile := filepath.Join(dir, "002_multi.sql")
	content := "CREATE TABLE " + table1 + " (id INT PRIMARY KEY);\nCREATE TABLE " + table2 + " (id INT PRIMARY KEY);"
	if err := os.WriteFile(sqlFile, []byte(content), 0644); err != nil {
		t.Fatalf("writing sql file: %v", err)
	}

	adapter := infra.NewMySQLDBAdapter(db)
	ctx := context.Background()

	if err := adapter.ApplySQLFromFile(ctx, sqlFile); err != nil {
		t.Fatalf("ApplySQLFromFile multi-statement: %v", err)
	}

	// Verify both tables exist.
	for _, tbl := range []string{table1, table2} {
		_, err = db.ExecContext(ctx, "INSERT INTO "+tbl+" (id) VALUES (1)")
		if err != nil {
			t.Fatalf("insert into %s: %v", tbl, err)
		}
	}
}

func TestEnsureSnapshotsTable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Cleanup(func() { testlib.DropTable(t, db, "joka_snapshots") })

	adapter := infra.NewMySQLDBAdapter(db)
	ctx := context.Background()

	// First call creates the table.
	if err := adapter.EnsureSnapshotsTable(ctx); err != nil {
		t.Fatalf("first EnsureSnapshotsTable: %v", err)
	}

	// Second call is idempotent.
	if err := adapter.EnsureSnapshotsTable(ctx); err != nil {
		t.Fatalf("second EnsureSnapshotsTable: %v", err)
	}
}

func TestCaptureAndGetSchemaSnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	userTable := "test_snapshot_users"
	t.Cleanup(func() {
		testlib.DropTable(t, db, "joka_snapshots")
		testlib.DropTable(t, db, userTable)
	})

	ctx := context.Background()

	// Create a user table so there's something to snapshot.
	_, err = db.ExecContext(ctx, "CREATE TABLE "+userTable+" (id INT PRIMARY KEY, email VARCHAR(255))")
	if err != nil {
		t.Fatalf("creating user table: %v", err)
	}

	adapter := infra.NewMySQLDBAdapter(db)

	if err := adapter.CaptureSchemaSnapshot(ctx, "240101120000"); err != nil {
		t.Fatalf("CaptureSchemaSnapshot: %v", err)
	}

	snapshot, err := adapter.GetSchemaSnapshot(ctx, "240101120000")
	if err != nil {
		t.Fatalf("GetSchemaSnapshot: %v", err)
	}

	// Parse snapshot JSON and verify it contains the user table.
	var schema map[string]string
	if err := json.Unmarshal([]byte(snapshot), &schema); err != nil {
		t.Fatalf("unmarshaling snapshot: %v", err)
	}

	createStmt, ok := schema[userTable]
	if !ok {
		t.Fatalf("snapshot missing table %s, got keys: %v", userTable, keys(schema))
	}
	if createStmt == "" {
		t.Fatal("expected non-empty CREATE TABLE statement")
	}
}

func TestGetLatestSnapshotIndex(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Cleanup(func() { testlib.DropTable(t, db, "joka_snapshots") })

	adapter := infra.NewMySQLDBAdapter(db)
	ctx := context.Background()

	if err := adapter.EnsureSnapshotsTable(ctx); err != nil {
		t.Fatalf("EnsureSnapshotsTable: %v", err)
	}

	// Insert two snapshots directly.
	_, err = db.ExecContext(ctx, "INSERT INTO joka_snapshots (migration_index, schema_snapshot) VALUES (?, ?)", "240101120000", "{}")
	if err != nil {
		t.Fatalf("inserting snapshot 1: %v", err)
	}
	_, err = db.ExecContext(ctx, "INSERT INTO joka_snapshots (migration_index, schema_snapshot) VALUES (?, ?)", "240202120000", "{}")
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
}

func keys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
