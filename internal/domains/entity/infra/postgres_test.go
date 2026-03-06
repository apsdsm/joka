package infra_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
	"github.com/apsdsm/joka/internal/domains/entity/infra"
	"github.com/apsdsm/joka/testlib"
)

// createPostgresTestTable creates a simple table for entity tests and registers cleanup.
func createPostgresTestTable(t *testing.T, db *sql.DB, name string) {
	t.Helper()

	ctx := context.Background()
	_, err := db.ExecContext(ctx, `CREATE TABLE "`+name+`" (id BIGSERIAL PRIMARY KEY, name VARCHAR(100), email VARCHAR(255))`)

	if err != nil {
		t.Fatalf("creating test table %s: %v", name, err)
	}

	t.Cleanup(func() { testlib.DropTablePostgres(t, db, name) })
}

func TestPostgresEnsureTrackingTable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Cleanup(func() { testlib.DropTablePostgres(t, db, "joka_entities") })

	adapter := infra.NewPostgresDBAdapter(db)
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

func TestPostgresIsEntitySynced_NotSynced(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Cleanup(func() { testlib.DropTablePostgres(t, db, "joka_entities") })

	adapter := infra.NewPostgresDBAdapter(db)
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

func TestPostgresRecordAndCheckEntitySynced(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Cleanup(func() { testlib.DropTablePostgres(t, db, "joka_entities") })

	adapter := infra.NewPostgresDBAdapter(db)
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

func TestPostgresInsertRow_ReturnsLastInsertId(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	tableName := "test_pg_entity_insert"
	createPostgresTestTable(t, db, tableName)

	adapter := infra.NewPostgresDBAdapter(db)
	ctx := context.Background()

	id1, err := adapter.InsertRow(ctx, tableName, map[string]any{
		"name":  "Alice",
		"email": "alice@test.com",
	}, "id")
	if err != nil {
		t.Fatalf("first InsertRow: %v", err)
	}

	if id1 != 1 {
		t.Errorf("expected first id 1, got %d", id1)
	}

	id2, err := adapter.InsertRow(ctx, tableName, map[string]any{
		"name":  "Bob",
		"email": "bob@test.com",
	}, "id")
	if err != nil {
		t.Fatalf("second InsertRow: %v", err)
	}

	if id2 != 2 {
		t.Errorf("expected second id 2, got %d", id2)
	}

	// Verify data exists.
	var name string

	err = db.QueryRowContext(ctx, `SELECT name FROM "`+tableName+`" WHERE id = $1`, id1).Scan(&name)
	if err != nil {
		t.Fatalf("querying row: %v", err)
	}

	if name != "Alice" {
		t.Errorf("expected 'Alice', got %q", name)
	}
}

func TestPostgresInsertRow_InTransaction(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	tableName := "test_pg_entity_tx_insert"
	createPostgresTestTable(t, db, tableName)

	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("beginning tx: %v", err)
	}

	adapter := infra.NewPostgresTxDBAdapter(tx, db)

	id, err := adapter.InsertRow(ctx, tableName, map[string]any{
		"name":  "TxUser",
		"email": "tx@test.com",
	}, "id")
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

	err = db.QueryRowContext(ctx, `SELECT name FROM "`+tableName+`" WHERE id = $1`, id).Scan(&name)
	if err != nil {
		t.Fatalf("querying row: %v", err)
	}

	if name != "TxUser" {
		t.Errorf("expected 'TxUser', got %q", name)
	}
}

// createPostgresEntityTrackingTables creates joka_entities and joka_entity_rows for integration tests.
func createPostgresEntityTrackingTables(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()

	adapter := infra.NewPostgresDBAdapter(db)
	if err := adapter.EnsureTrackingTable(ctx); err != nil {
		t.Fatalf("EnsureTrackingTable: %v", err)
	}
	if err := adapter.EnsureContentHashColumn(ctx); err != nil {
		t.Fatalf("EnsureContentHashColumn: %v", err)
	}
	if err := adapter.EnsureRowTrackingTable(ctx); err != nil {
		t.Fatalf("EnsureRowTrackingTable: %v", err)
	}

	t.Cleanup(func() {
		testlib.DropTablePostgres(t, db, "joka_entity_rows")
		testlib.DropTablePostgres(t, db, "joka_entities")
	})
}

func TestPostgresEnsureRowTrackingTable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Cleanup(func() { testlib.DropTablePostgres(t, db, "joka_entity_rows") })

	adapter := infra.NewPostgresDBAdapter(db)
	ctx := context.Background()

	if err := adapter.EnsureRowTrackingTable(ctx); err != nil {
		t.Fatalf("first call: %v", err)
	}

	if err := adapter.EnsureRowTrackingTable(ctx); err != nil {
		t.Fatalf("second call (idempotent): %v", err)
	}
}

func TestPostgresEnsureContentHashColumn(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Cleanup(func() { testlib.DropTablePostgres(t, db, "joka_entities") })

	adapter := infra.NewPostgresDBAdapter(db)
	ctx := context.Background()

	if err := adapter.EnsureTrackingTable(ctx); err != nil {
		t.Fatalf("EnsureTrackingTable: %v", err)
	}

	if err := adapter.EnsureContentHashColumn(ctx); err != nil {
		t.Fatalf("first call: %v", err)
	}

	if err := adapter.EnsureContentHashColumn(ctx); err != nil {
		t.Fatalf("second call (idempotent): %v", err)
	}
}

func TestPostgresRecordEntitySyncedWithHash(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	createPostgresEntityTrackingTables(t, db)
	adapter := infra.NewPostgresDBAdapter(db)
	ctx := context.Background()

	if err := adapter.RecordEntitySyncedWithHash(ctx, "test.yaml", "abc123"); err != nil {
		t.Fatalf("RecordEntitySyncedWithHash: %v", err)
	}

	hash, err := adapter.GetEntityHash(ctx, "test.yaml")
	if err != nil {
		t.Fatalf("GetEntityHash: %v", err)
	}

	if hash != "abc123" {
		t.Errorf("expected hash 'abc123', got %q", hash)
	}
}

func TestPostgresRecordEntitySyncedWithHash_Upsert(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	createPostgresEntityTrackingTables(t, db)
	adapter := infra.NewPostgresDBAdapter(db)
	ctx := context.Background()

	if err := adapter.RecordEntitySyncedWithHash(ctx, "upsert.yaml", "first"); err != nil {
		t.Fatalf("first call: %v", err)
	}

	if err := adapter.RecordEntitySyncedWithHash(ctx, "upsert.yaml", "second"); err != nil {
		t.Fatalf("second call: %v", err)
	}

	hash, err := adapter.GetEntityHash(ctx, "upsert.yaml")
	if err != nil {
		t.Fatalf("GetEntityHash: %v", err)
	}

	if hash != "second" {
		t.Errorf("expected hash 'second' after upsert, got %q", hash)
	}
}

func TestPostgresUpdateEntitySynced(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	createPostgresEntityTrackingTables(t, db)
	adapter := infra.NewPostgresDBAdapter(db)
	ctx := context.Background()

	if err := adapter.RecordEntitySyncedWithHash(ctx, "update.yaml", "old"); err != nil {
		t.Fatalf("RecordEntitySyncedWithHash: %v", err)
	}

	if err := adapter.UpdateEntitySynced(ctx, "update.yaml", "new"); err != nil {
		t.Fatalf("UpdateEntitySynced: %v", err)
	}

	hash, err := adapter.GetEntityHash(ctx, "update.yaml")
	if err != nil {
		t.Fatalf("GetEntityHash: %v", err)
	}

	if hash != "new" {
		t.Errorf("expected hash 'new', got %q", hash)
	}
}

func TestPostgresGetEntityHash(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	createPostgresEntityTrackingTables(t, db)
	adapter := infra.NewPostgresDBAdapter(db)
	ctx := context.Background()

	if err := adapter.RecordEntitySyncedWithHash(ctx, "gethash.yaml", "myhash"); err != nil {
		t.Fatalf("RecordEntitySyncedWithHash: %v", err)
	}

	hash, err := adapter.GetEntityHash(ctx, "gethash.yaml")
	if err != nil {
		t.Fatalf("GetEntityHash: %v", err)
	}

	if hash != "myhash" {
		t.Errorf("expected 'myhash', got %q", hash)
	}
}

func TestPostgresGetEntityHash_NotFound(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	createPostgresEntityTrackingTables(t, db)
	adapter := infra.NewPostgresDBAdapter(db)
	ctx := context.Background()

	hash, err := adapter.GetEntityHash(ctx, "nonexistent.yaml")
	if err != nil {
		t.Fatalf("GetEntityHash: %v", err)
	}

	if hash != "" {
		t.Errorf("expected empty string for unknown file, got %q", hash)
	}
}

func TestPostgresGetAllSyncedEntities(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	createPostgresEntityTrackingTables(t, db)
	adapter := infra.NewPostgresDBAdapter(db)
	ctx := context.Background()

	adapter.RecordEntitySyncedWithHash(ctx, "a.yaml", "hash_a")
	adapter.RecordEntitySyncedWithHash(ctx, "b.yaml", "hash_b")

	result, err := adapter.GetAllSyncedEntities(ctx)
	if err != nil {
		t.Fatalf("GetAllSyncedEntities: %v", err)
	}

	if len(result) < 2 {
		t.Fatalf("expected at least 2 entries, got %d", len(result))
	}

	if result["a.yaml"] != "hash_a" {
		t.Errorf("expected hash_a for a.yaml, got %q", result["a.yaml"])
	}

	if result["b.yaml"] != "hash_b" {
		t.Errorf("expected hash_b for b.yaml, got %q", result["b.yaml"])
	}
}

func TestPostgresRecordEntityRow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	createPostgresEntityTrackingTables(t, db)
	adapter := infra.NewPostgresDBAdapter(db)
	ctx := context.Background()

	row := domain.TrackedRow{
		EntityFile:     "test.yaml",
		TableName:      "users",
		RowPK:          42,
		PKColumn:       "id",
		RefID:          "admin",
		InsertionOrder: 0,
	}

	if err := adapter.RecordEntityRow(ctx, row); err != nil {
		t.Fatalf("RecordEntityRow: %v", err)
	}

	var tableName string
	var rowPK int64
	err = db.QueryRowContext(ctx,
		`SELECT table_name, row_pk FROM joka_entity_rows WHERE entity_file = $1`, "test.yaml",
	).Scan(&tableName, &rowPK)
	if err != nil {
		t.Fatalf("querying: %v", err)
	}

	if tableName != "users" || rowPK != 42 {
		t.Errorf("expected users/42, got %s/%d", tableName, rowPK)
	}
}

func TestPostgresGetTrackedRows(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	createPostgresEntityTrackingTables(t, db)
	adapter := infra.NewPostgresDBAdapter(db)
	ctx := context.Background()

	adapter.RecordEntityRow(ctx, domain.TrackedRow{EntityFile: "multi.yaml", TableName: "users", RowPK: 1, PKColumn: "id", InsertionOrder: 0})
	adapter.RecordEntityRow(ctx, domain.TrackedRow{EntityFile: "multi.yaml", TableName: "profiles", RowPK: 2, PKColumn: "id", InsertionOrder: 1})

	rows, err := adapter.GetTrackedRows(ctx, "multi.yaml")
	if err != nil {
		t.Fatalf("GetTrackedRows: %v", err)
	}

	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}

	if rows[0].InsertionOrder != 1 {
		t.Errorf("expected first row order 1, got %d", rows[0].InsertionOrder)
	}

	if rows[1].InsertionOrder != 0 {
		t.Errorf("expected second row order 0, got %d", rows[1].InsertionOrder)
	}
}

func TestPostgresGetTrackedRows_Empty(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	createPostgresEntityTrackingTables(t, db)
	adapter := infra.NewPostgresDBAdapter(db)
	ctx := context.Background()

	rows, err := adapter.GetTrackedRows(ctx, "empty.yaml")
	if err != nil {
		t.Fatalf("GetTrackedRows: %v", err)
	}

	if len(rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(rows))
	}
}

func TestPostgresDeleteTrackedRows(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	createPostgresEntityTrackingTables(t, db)
	adapter := infra.NewPostgresDBAdapter(db)
	ctx := context.Background()

	adapter.RecordEntityRow(ctx, domain.TrackedRow{EntityFile: "del.yaml", TableName: "users", RowPK: 1, PKColumn: "id", InsertionOrder: 0})
	adapter.RecordEntityRow(ctx, domain.TrackedRow{EntityFile: "del.yaml", TableName: "profiles", RowPK: 2, PKColumn: "id", InsertionOrder: 1})

	if err := adapter.DeleteTrackedRows(ctx, "del.yaml"); err != nil {
		t.Fatalf("DeleteTrackedRows: %v", err)
	}

	rows, err := adapter.GetTrackedRows(ctx, "del.yaml")
	if err != nil {
		t.Fatalf("GetTrackedRows: %v", err)
	}

	if len(rows) != 0 {
		t.Errorf("expected 0 rows after delete, got %d", len(rows))
	}
}

func TestPostgresDeleteRow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	tableName := "test_pg_delete_row"
	createPostgresTestTable(t, db, tableName)

	adapter := infra.NewPostgresDBAdapter(db)
	ctx := context.Background()

	id, err := adapter.InsertRow(ctx, tableName, map[string]any{"name": "ToDelete", "email": "del@test.com"}, "id")
	if err != nil {
		t.Fatalf("InsertRow: %v", err)
	}

	if err := adapter.DeleteRow(ctx, tableName, "id", id); err != nil {
		t.Fatalf("DeleteRow: %v", err)
	}

	var count int
	db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM "%s" WHERE id = $1`, tableName), id).Scan(&count)
	if count != 0 {
		t.Errorf("expected row to be deleted, count=%d", count)
	}
}

func TestPostgresDeleteRow_FKConflict(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	ctx := context.Background()

	db.ExecContext(ctx, `CREATE TABLE "test_pg_fk_parent" (id BIGSERIAL PRIMARY KEY, name VARCHAR(100))`)
	db.ExecContext(ctx, `CREATE TABLE "test_pg_fk_child" (id BIGSERIAL PRIMARY KEY, parent_id BIGINT NOT NULL REFERENCES "test_pg_fk_parent"(id))`)
	t.Cleanup(func() {
		testlib.DropTablePostgres(t, db, "test_pg_fk_child")
		testlib.DropTablePostgres(t, db, "test_pg_fk_parent")
	})

	adapter := infra.NewPostgresDBAdapter(db)

	parentID, err := adapter.InsertRow(ctx, "test_pg_fk_parent", map[string]any{"name": "Parent"}, "id")
	if err != nil {
		t.Fatalf("InsertRow parent: %v", err)
	}

	_, err = adapter.InsertRow(ctx, "test_pg_fk_child", map[string]any{"parent_id": parentID}, "id")
	if err != nil {
		t.Fatalf("InsertRow child: %v", err)
	}

	err = adapter.DeleteRow(ctx, "test_pg_fk_parent", "id", parentID)
	if err == nil {
		t.Fatal("expected FK error, got nil")
	}

	if !errorIs(err, domain.ErrForeignKeyConflict) {
		t.Errorf("expected ErrForeignKeyConflict, got: %v", err)
	}
}

func TestPostgresDeleteEntityRecord(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	createPostgresEntityTrackingTables(t, db)
	adapter := infra.NewPostgresDBAdapter(db)
	ctx := context.Background()

	adapter.RecordEntitySyncedWithHash(ctx, "toremove.yaml", "hash")

	synced, _ := adapter.IsEntitySynced(ctx, "toremove.yaml")
	if !synced {
		t.Fatal("expected file to be synced before delete")
	}

	if err := adapter.DeleteEntityRecord(ctx, "toremove.yaml"); err != nil {
		t.Fatalf("DeleteEntityRecord: %v", err)
	}

	synced, _ = adapter.IsEntitySynced(ctx, "toremove.yaml")
	if synced {
		t.Error("expected file to be unsynced after delete")
	}
}

func TestPostgresLookupValue(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	tableName := "test_pg_lookup"
	createPostgresTestTable(t, db, tableName)

	adapter := infra.NewPostgresDBAdapter(db)
	ctx := context.Background()

	adapter.InsertRow(ctx, tableName, map[string]any{"name": "LookupUser", "email": "lookup@test.com"}, "id")

	val, err := adapter.LookupValue(ctx, tableName, "email", "name", "LookupUser")
	if err != nil {
		t.Fatalf("LookupValue: %v", err)
	}

	email, ok := val.(string)
	if !ok {
		t.Fatalf("expected string, got %T", val)
	}

	if email != "lookup@test.com" {
		t.Errorf("expected 'lookup@test.com', got %q", email)
	}
}
