package infra_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/apsdsm/joka/internal/domains/migration/infra"
	"github.com/apsdsm/joka/testlib"
)

// execPG runs a statement and fails the test on error.
func execPG(t *testing.T, db *sql.DB, stmt string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), stmt); err != nil {
		t.Fatalf("exec %q: %v", stmt, err)
	}
}

// TestPostgresComputeSchemaFidelity guards the reconstruction `migrate verify`
// compares against. It used to lose identity columns and print the placeholders
// ARRAY / USER-DEFINED for real types, which made drift detection blind to the
// exact changes it exists to catch (see bug_report_20260807.md).
//
// Consolidation no longer reads this — it dumps the schema with pg_dump — but
// snapshots and drift detection still do.
func TestPostgresComputeSchemaFidelity(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}
	ctx := context.Background()

	t.Cleanup(func() {
		testlib.DropTablePostgres(t, db, "test_pg_fid_child")
		testlib.DropTablePostgres(t, db, "test_pg_fid_parent")
	})

	execPG(t, db, `CREATE TABLE test_pg_fid_parent (
		id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		tags text[],
		amt numeric(10,2),
		code varchar(20) NOT NULL,
		created_at timestamptz DEFAULT now()
	)`)
	execPG(t, db, `CREATE INDEX test_pg_fid_tags_idx ON test_pg_fid_parent USING gin (tags)`)
	execPG(t, db, `CREATE TABLE test_pg_fid_child (
		id serial PRIMARY KEY,
		parent_id bigint REFERENCES test_pg_fid_parent(id),
		n int CHECK (n > 0),
		doubled int GENERATED ALWAYS AS (n * 2) STORED
	)`)

	adapter := infra.NewPostgresDBAdapter(db)
	schema, err := adapter.ComputeSchema(ctx)
	if err != nil {
		t.Fatalf("ComputeSchema: %v", err)
	}

	parent := schema["test_pg_fid_parent"]
	child := schema["test_pg_fid_child"]

	t.Run("it terminates the CREATE TABLE before appending index statements", func(t *testing.T) {
		if !strings.Contains(parent, "CREATE INDEX") {
			t.Fatalf("expected an index statement after the table body, got:\n%s", parent)
		}
		if !strings.Contains(parent, ");\nCREATE INDEX") {
			t.Errorf("expected the table body to be terminated with ');' before the index, got:\n%s", parent)
		}
	})

	t.Run("it emits index statements without a hardcoded schema qualifier", func(t *testing.T) {
		if strings.Contains(parent, "ON public.") {
			t.Errorf("expected index to be schema-unqualified, got:\n%s", parent)
		}
	})

	t.Run("it renders array columns as their element type", func(t *testing.T) {
		if !strings.Contains(parent, "tags text[]") {
			t.Errorf("expected 'tags text[]', got:\n%s", parent)
		}
		if strings.Contains(parent, "ARRAY") {
			t.Errorf("expected no ARRAY placeholder, got:\n%s", parent)
		}
	})

	t.Run("it preserves identity columns", func(t *testing.T) {
		if !strings.Contains(parent, "GENERATED ALWAYS AS IDENTITY") {
			t.Errorf("expected identity to be preserved, got:\n%s", parent)
		}
	})

	t.Run("it preserves parameterised types", func(t *testing.T) {
		if !strings.Contains(parent, "amt numeric(10,2)") {
			t.Errorf("expected 'amt numeric(10,2)', got:\n%s", parent)
		}
		if !strings.Contains(parent, "code character varying(20) NOT NULL") {
			t.Errorf("expected 'code character varying(20) NOT NULL', got:\n%s", parent)
		}
	})

	t.Run("it renders serial columns as serial rather than a bare nextval default", func(t *testing.T) {
		if !strings.Contains(child, "id serial") {
			t.Errorf("expected 'id serial', got:\n%s", child)
		}
		if strings.Contains(child, "nextval(") {
			t.Errorf("expected no nextval default (the sequence would not exist), got:\n%s", child)
		}
	})

	t.Run("it preserves generated columns", func(t *testing.T) {
		if !strings.Contains(child, "GENERATED ALWAYS AS ((n * 2)) STORED") {
			t.Errorf("expected the stored generated column, got:\n%s", child)
		}
	})

	t.Run("the reconstruction is valid SQL", func(t *testing.T) {
		// Not how baselines are built any more, but a reconstruction that cannot
		// be applied is a reconstruction that got the schema wrong.
		script := parent + "\n" + child + "\n"
		if err := testlib.ApplyToScratchPostgresDB(t, script, "joka_fidelity_replay"); err != nil {
			t.Fatalf("reconstruction did not apply:\n%v", err)
		}
	})
}

func TestPostgresComputeSchemaSkipsViews(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}
	ctx := context.Background()

	t.Cleanup(func() {
		execPG(t, db, `DROP VIEW IF EXISTS test_pg_view_v`)
		testlib.DropTablePostgres(t, db, "test_pg_view_base")
	})

	execPG(t, db, `CREATE TABLE test_pg_view_base (id int PRIMARY KEY, name text)`)
	execPG(t, db, `CREATE VIEW test_pg_view_v AS SELECT id FROM test_pg_view_base`)

	adapter := infra.NewPostgresDBAdapter(db)
	schema, err := adapter.ComputeSchema(ctx)
	if err != nil {
		t.Fatalf("ComputeSchema: %v", err)
	}

	if _, ok := schema["test_pg_view_v"]; ok {
		t.Errorf("expected the view to be excluded from the table snapshot, got keys: %v", pgKeys(schema))
	}
	if _, ok := schema["test_pg_view_base"]; !ok {
		t.Errorf("expected the base table in the snapshot, got keys: %v", pgKeys(schema))
	}
}
