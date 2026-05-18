package infra_test

import (
	"context"
	"testing"

	"github.com/apsdsm/joka/internal/domains/dbtools/infra"
	"github.com/apsdsm/joka/testlib"
)

func TestMySQLListTables(t *testing.T) {
	db, err := testlib.GetTestDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}
	ctx := context.Background()

	t.Run("it returns user tables in alphabetical order", func(t *testing.T) {
		_, err := db.ExecContext(ctx, "CREATE TABLE `dbtools_list_a` (id INT)")
		if err != nil {
			t.Fatalf("creating table a: %v", err)
		}
		t.Cleanup(func() { testlib.DropTable(t, db, "dbtools_list_a") })

		_, err = db.ExecContext(ctx, "CREATE TABLE `dbtools_list_b` (id INT)")
		if err != nil {
			t.Fatalf("creating table b: %v", err)
		}
		t.Cleanup(func() { testlib.DropTable(t, db, "dbtools_list_b") })

		adapter := infra.NewMySQLDBAdapter(db)
		tables, err := adapter.ListTables(ctx)
		if err != nil {
			t.Fatalf("ListTables: %v", err)
		}

		// Other tests may leave tables around; just check ours are present in order.
		var idxA, idxB = -1, -1
		for i, name := range tables {
			if name == "dbtools_list_a" {
				idxA = i
			}
			if name == "dbtools_list_b" {
				idxB = i
			}
		}
		if idxA == -1 || idxB == -1 {
			t.Fatalf("expected dbtools_list_a and dbtools_list_b in %v", tables)
		}
		if idxA > idxB {
			t.Errorf("expected dbtools_list_a before dbtools_list_b, got %v", tables)
		}
	})
}

func TestMySQLDropAllTables(t *testing.T) {
	db, err := testlib.GetTestDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}
	ctx := context.Background()

	t.Run("it drops every table, including ones tied by foreign keys", func(t *testing.T) {
		// Clean slate.
		adapter := infra.NewMySQLDBAdapter(db)
		if err := adapter.DropAllTables(ctx); err != nil {
			t.Fatalf("initial drop: %v", err)
		}

		_, err := db.ExecContext(ctx, "CREATE TABLE `dbtools_parent` (id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY)")
		if err != nil {
			t.Fatalf("creating parent: %v", err)
		}
		t.Cleanup(func() { testlib.DropTable(t, db, "dbtools_parent") })

		_, err = db.ExecContext(ctx, `CREATE TABLE `+"`dbtools_child`"+` (
			id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
			parent_id BIGINT UNSIGNED NOT NULL,
			CONSTRAINT fk_dbtools_child FOREIGN KEY (parent_id) REFERENCES dbtools_parent(id)
		)`)
		if err != nil {
			t.Fatalf("creating child: %v", err)
		}
		t.Cleanup(func() { testlib.DropTable(t, db, "dbtools_child") })

		if err := adapter.DropAllTables(ctx); err != nil {
			t.Fatalf("DropAllTables: %v", err)
		}

		remaining, err := adapter.ListTables(ctx)
		if err != nil {
			t.Fatalf("ListTables after drop: %v", err)
		}
		if len(remaining) != 0 {
			t.Errorf("expected zero tables after drop, got %v", remaining)
		}
	})

	t.Run("it is a no-op when no tables exist", func(t *testing.T) {
		adapter := infra.NewMySQLDBAdapter(db)
		// Ensure clean slate.
		if err := adapter.DropAllTables(ctx); err != nil {
			t.Fatalf("initial drop: %v", err)
		}

		if err := adapter.DropAllTables(ctx); err != nil {
			t.Errorf("expected no error on empty DB, got %v", err)
		}
	})
}
