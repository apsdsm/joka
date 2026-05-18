package infra_test

import (
	"context"
	"testing"

	"github.com/apsdsm/joka/internal/domains/dbtools/infra"
	"github.com/apsdsm/joka/testlib"
)

func TestPostgresListTables(t *testing.T) {
	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}
	ctx := context.Background()

	t.Run("it returns user tables in alphabetical order", func(t *testing.T) {
		_, err := db.ExecContext(ctx, `CREATE TABLE "dbtools_pg_list_a" (id INT)`)
		if err != nil {
			t.Fatalf("creating table a: %v", err)
		}
		t.Cleanup(func() { testlib.DropTablePostgres(t, db, "dbtools_pg_list_a") })

		_, err = db.ExecContext(ctx, `CREATE TABLE "dbtools_pg_list_b" (id INT)`)
		if err != nil {
			t.Fatalf("creating table b: %v", err)
		}
		t.Cleanup(func() { testlib.DropTablePostgres(t, db, "dbtools_pg_list_b") })

		adapter := infra.NewPostgresDBAdapter(db)
		tables, err := adapter.ListTables(ctx)
		if err != nil {
			t.Fatalf("ListTables: %v", err)
		}

		var idxA, idxB = -1, -1
		for i, name := range tables {
			if name == "dbtools_pg_list_a" {
				idxA = i
			}
			if name == "dbtools_pg_list_b" {
				idxB = i
			}
		}
		if idxA == -1 || idxB == -1 {
			t.Fatalf("expected dbtools_pg_list_a and dbtools_pg_list_b in %v", tables)
		}
		if idxA > idxB {
			t.Errorf("expected dbtools_pg_list_a before dbtools_pg_list_b, got %v", tables)
		}
	})
}

func TestPostgresDropAllTables(t *testing.T) {
	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}
	ctx := context.Background()

	t.Run("it drops every table, including ones tied by foreign keys", func(t *testing.T) {
		adapter := infra.NewPostgresDBAdapter(db)
		if err := adapter.DropAllTables(ctx); err != nil {
			t.Fatalf("initial drop: %v", err)
		}

		_, err := db.ExecContext(ctx, `CREATE TABLE "dbtools_pg_parent" (id BIGSERIAL PRIMARY KEY)`)
		if err != nil {
			t.Fatalf("creating parent: %v", err)
		}
		t.Cleanup(func() { testlib.DropTablePostgres(t, db, "dbtools_pg_parent") })

		_, err = db.ExecContext(ctx, `CREATE TABLE "dbtools_pg_child" (
			id BIGSERIAL PRIMARY KEY,
			parent_id BIGINT NOT NULL,
			CONSTRAINT fk_dbtools_pg_child FOREIGN KEY (parent_id) REFERENCES "dbtools_pg_parent"(id)
		)`)
		if err != nil {
			t.Fatalf("creating child: %v", err)
		}
		t.Cleanup(func() { testlib.DropTablePostgres(t, db, "dbtools_pg_child") })

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
		adapter := infra.NewPostgresDBAdapter(db)
		if err := adapter.DropAllTables(ctx); err != nil {
			t.Fatalf("initial drop: %v", err)
		}

		if err := adapter.DropAllTables(ctx); err != nil {
			t.Errorf("expected no error on empty DB, got %v", err)
		}
	})
}
