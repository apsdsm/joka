package infra_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/apsdsm/joka/internal/domains/template/domain"
	"github.com/apsdsm/joka/internal/domains/template/infra"
	"github.com/apsdsm/joka/testlib"
)

// createPostgresTestTable creates a simple table for template tests and registers cleanup.
func createPostgresTestTable(t *testing.T, db *sql.DB, name string) {
	t.Helper()
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `CREATE TABLE "`+name+`" (id INTEGER PRIMARY KEY, name VARCHAR(100), email VARCHAR(255))`)
	if err != nil {
		t.Fatalf("creating test table %s: %v", name, err)
	}
	t.Cleanup(func() { testlib.DropTablePostgres(t, db, name) })
}

func TestPostgresTruncateTable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	tableName := "test_pg_tmpl_truncate"
	createPostgresTestTable(t, db, tableName)

	ctx := context.Background()

	// Insert some rows.
	_, err = db.ExecContext(ctx, `INSERT INTO "`+tableName+`" (id, name, email) VALUES (1, 'alice', 'alice@test.com'), (2, 'bob', 'bob@test.com')`)
	if err != nil {
		t.Fatalf("inserting rows: %v", err)
	}

	adapter := infra.NewPostgresDBAdapter(db)

	if err := adapter.TruncateTable(ctx, tableName); err != nil {
		t.Fatalf("TruncateTable: %v", err)
	}

	// Verify table is empty.
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+tableName+`"`).Scan(&count); err != nil {
		t.Fatalf("counting rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 rows after truncate, got %d", count)
	}
}

func TestPostgresTruncateTable_NotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	adapter := infra.NewPostgresDBAdapter(db)
	ctx := context.Background()

	err = adapter.TruncateTable(ctx, "nonexistent_table_xyz")
	if err == nil {
		t.Fatal("expected error for nonexistent table, got nil")
	}
	if !errors.Is(err, domain.ErrTableNotFound) {
		t.Fatalf("expected ErrTableNotFound, got: %v", err)
	}
}

func TestPostgresInsertRows(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	tableName := "test_pg_tmpl_insert"
	createPostgresTestTable(t, db, tableName)

	adapter := infra.NewPostgresDBAdapter(db)
	ctx := context.Background()

	rows := []map[string]any{
		{"id": 1, "name": "alice", "email": "alice@test.com"},
		{"id": 2, "name": "bob", "email": "bob@test.com"},
	}

	count, err := adapter.InsertRows(ctx, tableName, rows)
	if err != nil {
		t.Fatalf("InsertRows: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 inserted, got %d", count)
	}

	// Verify data.
	var name, email string
	err = db.QueryRowContext(ctx, `SELECT name, email FROM "`+tableName+`" WHERE id = 1`).Scan(&name, &email)
	if err != nil {
		t.Fatalf("querying inserted row: %v", err)
	}
	if name != "alice" || email != "alice@test.com" {
		t.Errorf("unexpected row data: name=%q email=%q", name, email)
	}
}

func TestPostgresInsertRows_EmptySlice(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	tableName := "test_pg_tmpl_empty"
	createPostgresTestTable(t, db, tableName)

	adapter := infra.NewPostgresDBAdapter(db)
	ctx := context.Background()

	count, err := adapter.InsertRows(ctx, tableName, []map[string]any{})
	if err != nil {
		t.Fatalf("InsertRows with empty slice: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 inserted, got %d", count)
	}
}
