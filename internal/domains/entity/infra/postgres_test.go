package infra_test

import (
	"context"
	"database/sql"
	"testing"

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

// createPostgresEntityTrackingTables prepares the tracking a mutating entity
// command expects: the state document's table, and none of the decomposed ones
// tracking version 3 replaced.
func createPostgresEntityTrackingTables(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()

	for _, table := range []string{"joka_state", "joka_meta", "joka_entity_rows", "joka_entities"} {
		testlib.DropTablePostgres(t, db, table)
	}

	if err := infra.NewPostgresStateBackend(db).EnsureStateTable(ctx); err != nil {
		t.Fatalf("EnsureStateTable: %v", err)
	}

	t.Cleanup(func() { testlib.DropTablePostgres(t, db, "joka_state") })
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

	t.Run("it creates the table and is idempotent", func(t *testing.T) {
		adapter := infra.NewPostgresDBAdapter(db)
		ctx := context.Background()

		if err := adapter.EnsureTrackingTable(ctx); err != nil {
			t.Fatalf("first EnsureTrackingTable: %v", err)
		}

		if err := adapter.EnsureTrackingTable(ctx); err != nil {
			t.Fatalf("second EnsureTrackingTable: %v", err)
		}
	})
}

func TestPostgresInsertRow(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Run("it returns sequential auto-increment IDs", func(t *testing.T) {
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

		var name string

		err = db.QueryRowContext(ctx, `SELECT name FROM "`+tableName+`" WHERE id = $1`, id1).Scan(&name)
		if err != nil {
			t.Fatalf("querying row: %v", err)
		}

		if name != "Alice" {
			t.Errorf("expected 'Alice', got %q", name)
		}
	})

	t.Run("it inserts within a transaction", func(t *testing.T) {
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

		var name string

		err = db.QueryRowContext(ctx, `SELECT name FROM "`+tableName+`" WHERE id = $1`, id).Scan(&name)
		if err != nil {
			t.Fatalf("querying row: %v", err)
		}

		if name != "TxUser" {
			t.Errorf("expected 'TxUser', got %q", name)
		}
	})

	t.Run("it returns the natural-key value when _pk is in columns", func(t *testing.T) {
		tableName := "test_pg_entity_join"
		ctx := context.Background()

		_, err := db.ExecContext(ctx,
			`CREATE TABLE "`+tableName+`" (client_id BIGINT NOT NULL, api_key_id BIGINT NOT NULL UNIQUE)`,
		)
		if err != nil {
			t.Fatalf("creating join table: %v", err)
		}
		t.Cleanup(func() { testlib.DropTablePostgres(t, db, tableName) })

		adapter := infra.NewPostgresDBAdapter(db)

		got, err := adapter.InsertRow(ctx, tableName, map[string]any{
			"client_id":  int64(7),
			"api_key_id": int64(42),
		}, "api_key_id")
		if err != nil {
			t.Fatalf("InsertRow: %v", err)
		}

		if got != 42 {
			t.Errorf("expected tracked PK 42 (from columns), got %d", got)
		}
	})

	t.Run("it fails loudly when the table has no auto-increment and no _pk is set", func(t *testing.T) {
		tableName := "test_pg_entity_join_no_pk"
		ctx := context.Background()

		_, err := db.ExecContext(ctx,
			`CREATE TABLE "`+tableName+`" (client_id BIGINT NOT NULL, api_key_id BIGINT NOT NULL)`,
		)
		if err != nil {
			t.Fatalf("creating join table: %v", err)
		}
		t.Cleanup(func() { testlib.DropTablePostgres(t, db, tableName) })

		adapter := infra.NewPostgresDBAdapter(db)

		_, err = adapter.InsertRow(ctx, tableName, map[string]any{
			"client_id":  int64(1),
			"api_key_id": int64(2),
		}, "id")
		if err == nil {
			t.Fatal("expected error from InsertRow with no auto-increment and no _pk, got nil")
		}
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

	t.Run("it creates the table and is idempotent", func(t *testing.T) {
		adapter := infra.NewPostgresDBAdapter(db)
		ctx := context.Background()

		if err := adapter.EnsureRowTrackingTable(ctx); err != nil {
			t.Fatalf("first call: %v", err)
		}

		if err := adapter.EnsureRowTrackingTable(ctx); err != nil {
			t.Fatalf("second call (idempotent): %v", err)
		}
	})
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

	t.Run("it adds the column and is idempotent", func(t *testing.T) {
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
	})
}

func TestPostgresLookupValue(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Run("it returns the matching column value", func(t *testing.T) {
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
	})
}

func TestUpdateRowPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Run("it updates the given columns and leaves the primary key untouched", func(t *testing.T) {
		tableName := "test_entity_update_pg"
		createPostgresTestTable(t, db, tableName)

		adapter := infra.NewPostgresDBAdapter(db)
		ctx := context.Background()

		id, err := adapter.InsertRow(ctx, tableName, map[string]any{
			"name":  "Before",
			"email": "before@test.com",
		}, "id")
		if err != nil {
			t.Fatalf("InsertRow: %v", err)
		}

		// Include the pk column in the update map; it must be ignored.
		err = adapter.UpdateRow(ctx, tableName, "id", id, map[string]any{
			"id":    int64(999),
			"name":  "After",
			"email": "after@test.com",
		})
		if err != nil {
			t.Fatalf("UpdateRow: %v", err)
		}

		var name, email string
		err = db.QueryRowContext(ctx, `SELECT name, email FROM "`+tableName+`" WHERE id = $1`, id).Scan(&name, &email)
		if err != nil {
			t.Fatalf("querying row (pk should be unchanged): %v", err)
		}

		if name != "After" || email != "after@test.com" {
			t.Errorf("expected updated values, got name=%q email=%q", name, email)
		}
	})
}

func TestGetRowPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Run("it reads the requested columns as comparable values", func(t *testing.T) {
		tableName := "test_entity_getrow_pg"
		createPostgresTestTable(t, db, tableName)

		adapter := infra.NewPostgresDBAdapter(db)
		ctx := context.Background()

		id, err := adapter.InsertRow(ctx, tableName, map[string]any{"name": "Alice", "email": "alice@test.com"}, "id")
		if err != nil {
			t.Fatalf("InsertRow: %v", err)
		}

		got, err := adapter.GetRow(ctx, tableName, []string{"name", "email"}, "id", id)
		if err != nil {
			t.Fatalf("GetRow: %v", err)
		}

		if got["name"] != "Alice" {
			t.Errorf("expected name 'Alice', got %#v", got["name"])
		}
		if got["email"] != "alice@test.com" {
			t.Errorf("expected email string, got %#v", got["email"])
		}
	})
}

func TestPostgresRowAndTableExists(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	ctx := context.Background()
	createPostgresTestTable(t, db, "exists_test")

	adapter := infra.NewPostgresDBAdapter(db)

	var id int64
	if err := db.QueryRowContext(ctx,
		`INSERT INTO "exists_test" (name) VALUES ('kept') RETURNING id`,
	).Scan(&id); err != nil {
		t.Fatalf("seeding row: %v", err)
	}

	t.Run("it finds a row that is present", func(t *testing.T) {
		live, err := adapter.RowExists(ctx, "exists_test", "id", id)
		if err != nil {
			t.Fatalf("RowExists: %v", err)
		}
		if !live {
			t.Error("expected the seeded row to be found")
		}
	})

	t.Run("it reports a missing row without erroring", func(t *testing.T) {
		live, err := adapter.RowExists(ctx, "exists_test", "id", id+9999)
		if err != nil {
			t.Fatalf("RowExists: %v", err)
		}
		if live {
			t.Error("expected a primary key that was never inserted to be absent")
		}
	})

	t.Run("it reports whether a table exists", func(t *testing.T) {
		exists, err := adapter.TableExists(ctx, "exists_test")
		if err != nil {
			t.Fatalf("TableExists: %v", err)
		}
		if !exists {
			t.Error("expected exists_test to exist")
		}

		exists, err = adapter.TableExists(ctx, "never_created_table")
		if err != nil {
			t.Fatalf("TableExists: %v", err)
		}
		if exists {
			t.Error("expected a table that was never created to be absent")
		}
	})
}

// mustRecord fails the test when tracking a row fails. These calls used to
// ignore the error, which hid an insert the ref_id constraint was rejecting.
func mustRecord(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("RecordEntityRow: %v", err)
	}
}
