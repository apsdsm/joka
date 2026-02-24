package infra_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/infra"
	"github.com/apsdsm/joka/testlib"
)

// createTestTable creates a simple table for entity tests and registers cleanup.
func createTestTable(t *testing.T, db *sql.DB, name string) {
	t.Helper()

	ctx := context.Background()
	_, err := db.ExecContext(ctx, "CREATE TABLE `"+name+"` (id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, name VARCHAR(100), email VARCHAR(255))")

	if err != nil {
		t.Fatalf("creating test table %s: %v", name, err)
	}

	t.Cleanup(func() { testlib.DropTable(t, db, name) })
}

func TestEnsureTrackingTable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Cleanup(func() { testlib.DropTable(t, db, "joka_entities") })

	adapter := infra.NewMySQLDBAdapter(db)
	ctx := context.Background()

	// First call creates the table.
	if err := adapter.EnsureTrackingTable(ctx); err != nil {
		t.Fatalf("first EnsureTrackingTable: %v", err)
	}

	// Second call is idempotent.
	if err := adapter.EnsureTrackingTable(ctx); err != nil {
		t.Fatalf("second EnsureTrackingTable: %v", err)
	}
}

func TestIsEntitySynced_NotSynced(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Cleanup(func() { testlib.DropTable(t, db, "joka_entities") })

	adapter := infra.NewMySQLDBAdapter(db)
	ctx := context.Background()

	if err := adapter.EnsureTrackingTable(ctx); err != nil {
		t.Fatalf("EnsureTrackingTable: %v", err)
	}

	synced, err := adapter.IsEntitySynced(ctx, "test/file.yaml")
	if err != nil {
		t.Fatalf("IsEntitySynced: %v", err)
	}

	if synced {
		t.Error("expected false for unsynced file")
	}
}

func TestRecordAndCheckEntitySynced(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Cleanup(func() { testlib.DropTable(t, db, "joka_entities") })

	adapter := infra.NewMySQLDBAdapter(db)
	ctx := context.Background()

	if err := adapter.EnsureTrackingTable(ctx); err != nil {
		t.Fatalf("EnsureTrackingTable: %v", err)
	}

	if err := adapter.RecordEntitySynced(ctx, "persons/test.yaml"); err != nil {
		t.Fatalf("RecordEntitySynced: %v", err)
	}

	synced, err := adapter.IsEntitySynced(ctx, "persons/test.yaml")
	if err != nil {
		t.Fatalf("IsEntitySynced: %v", err)
	}

	if !synced {
		t.Error("expected true after recording sync")
	}

	// Different file should still be unsynced.
	synced, err = adapter.IsEntitySynced(ctx, "other/file.yaml")
	if err != nil {
		t.Fatalf("IsEntitySynced: %v", err)
	}

	if synced {
		t.Error("expected false for different file")
	}
}

func TestInsertRow_ReturnsLastInsertId(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	tableName := "test_entity_insert"
	createTestTable(t, db, tableName)

	adapter := infra.NewMySQLDBAdapter(db)
	ctx := context.Background()

	id1, err := adapter.InsertRow(ctx, tableName, map[string]any{
		"name":  "Alice",
		"email": "alice@test.com",
	})
	if err != nil {
		t.Fatalf("first InsertRow: %v", err)
	}

	if id1 != 1 {
		t.Errorf("expected first id 1, got %d", id1)
	}

	id2, err := adapter.InsertRow(ctx, tableName, map[string]any{
		"name":  "Bob",
		"email": "bob@test.com",
	})
	if err != nil {
		t.Fatalf("second InsertRow: %v", err)
	}

	if id2 != 2 {
		t.Errorf("expected second id 2, got %d", id2)
	}

	// Verify data exists.
	var name string

	err = db.QueryRowContext(ctx, "SELECT name FROM `"+tableName+"` WHERE id = ?", id1).Scan(&name)
	if err != nil {
		t.Fatalf("querying row: %v", err)
	}

	if name != "Alice" {
		t.Errorf("expected 'Alice', got %q", name)
	}
}

func TestInsertRow_InTransaction(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	tableName := "test_entity_tx_insert"
	createTestTable(t, db, tableName)

	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("beginning tx: %v", err)
	}

	adapter := infra.NewMySQLTxDBAdapter(tx, db)

	id, err := adapter.InsertRow(ctx, tableName, map[string]any{
		"name":  "TxUser",
		"email": "tx@test.com",
	})
	if err != nil {
		tx.Rollback() //nolint:errcheck
		t.Fatalf("InsertRow in tx: %v", err)
	}

	if id < 1 {
		t.Errorf("expected positive id, got %d", id)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("committing: %v", err)
	}

	// Verify data persisted.
	var name string

	err = db.QueryRowContext(ctx, "SELECT name FROM `"+tableName+"` WHERE id = ?", id).Scan(&name)
	if err != nil {
		t.Fatalf("querying row: %v", err)
	}

	if name != "TxUser" {
		t.Errorf("expected 'TxUser', got %q", name)
	}
}
