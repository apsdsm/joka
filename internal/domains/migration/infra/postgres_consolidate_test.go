package infra_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/apsdsm/joka/internal/domains/migration/app"
	"github.com/apsdsm/joka/internal/domains/migration/domain"
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

// TestPostgresComputeSchemaFidelity covers the reconstruction defects from the
// 2026-08-07 consolidate bug report: unterminated CREATE TABLE, ARRAY /
// USER-DEFINED placeholder types, dropped identity, and serial columns whose
// sequence is never created.
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

	t.Run("the consolidated SQL applies to an empty schema", func(t *testing.T) {
		subset := map[string]string{
			"test_pg_fid_parent": parent,
			"test_pg_fid_child":  child,
		}
		order, err := app.TopologicalSort(app.ParseFKDependencies(subset))
		if err != nil {
			t.Fatalf("TopologicalSort: %v", err)
		}

		consolidated := app.GenerateConsolidatedSQL(subset, order)
		if err := adapter.ValidateSchemaSQL(ctx, consolidated); err != nil {
			t.Fatalf("generated SQL did not apply:\n%v\n\ngenerated:\n%s", err, consolidated)
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

func TestPostgresUnsupportedSchemaObjects(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}
	ctx := context.Background()
	adapter := infra.NewPostgresDBAdapter(db)

	t.Run("it reports nothing for a schema of plain tables", func(t *testing.T) {
		t.Cleanup(func() { testlib.DropTablePostgres(t, db, "test_pg_unsup_plain") })
		execPG(t, db, `CREATE TABLE test_pg_unsup_plain (id serial PRIMARY KEY, name text)`)

		objects, err := adapter.UnsupportedSchemaObjects(ctx)
		if err != nil {
			t.Fatalf("UnsupportedSchemaObjects: %v", err)
		}
		if len(objects) != 0 {
			t.Errorf("expected no unsupported objects (the serial's own sequence does not count), got %v", objects)
		}
	})

	t.Run("it reports views, types, functions, sequences and triggers", func(t *testing.T) {
		t.Cleanup(func() {
			execPG(t, db, `DROP VIEW IF EXISTS test_pg_unsup_view`)
			execPG(t, db, `DROP TRIGGER IF EXISTS test_pg_unsup_trg ON test_pg_unsup_base`)
			testlib.DropTablePostgres(t, db, "test_pg_unsup_base")
			execPG(t, db, `DROP FUNCTION IF EXISTS test_pg_unsup_fn()`)
			execPG(t, db, `DROP TYPE IF EXISTS test_pg_unsup_mood`)
			execPG(t, db, `DROP SEQUENCE IF EXISTS test_pg_unsup_seq`)
		})

		execPG(t, db, `CREATE TYPE test_pg_unsup_mood AS ENUM ('ok', 'bad')`)
		execPG(t, db, `CREATE SEQUENCE test_pg_unsup_seq`)
		execPG(t, db, `CREATE TABLE test_pg_unsup_base (id int PRIMARY KEY, m test_pg_unsup_mood)`)
		execPG(t, db, `CREATE VIEW test_pg_unsup_view AS SELECT id FROM test_pg_unsup_base`)
		execPG(t, db, `CREATE FUNCTION test_pg_unsup_fn() RETURNS trigger AS $$ BEGIN RETURN NEW; END; $$ LANGUAGE plpgsql`)
		execPG(t, db, `CREATE TRIGGER test_pg_unsup_trg BEFORE INSERT ON test_pg_unsup_base FOR EACH ROW EXECUTE FUNCTION test_pg_unsup_fn()`)

		objects, err := adapter.UnsupportedSchemaObjects(ctx)
		if err != nil {
			t.Fatalf("UnsupportedSchemaObjects: %v", err)
		}

		joined := strings.Join(objects, "\n")
		for _, want := range []string{
			"view test_pg_unsup_view",
			"enum type test_pg_unsup_mood",
			"function test_pg_unsup_fn",
			"sequence test_pg_unsup_seq",
			"trigger test_pg_unsup_trg on test_pg_unsup_base",
		} {
			if !strings.Contains(joined, want) {
				t.Errorf("expected %q to be reported, got:\n%s", want, joined)
			}
		}
	})
}

func TestPostgresValidateSchemaSQL(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}
	ctx := context.Background()
	adapter := infra.NewPostgresDBAdapter(db)

	t.Run("it accepts applicable SQL and leaves nothing behind", func(t *testing.T) {
		err := adapter.ValidateSchemaSQL(ctx, "CREATE TABLE test_pg_validate_ok (id int PRIMARY KEY);")
		if err != nil {
			t.Fatalf("expected valid SQL to pass, got: %v", err)
		}

		var count int
		row := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relname = 'test_pg_validate_ok'`)
		if err := row.Scan(&count); err != nil {
			t.Fatalf("counting leftover tables: %v", err)
		}
		if count != 0 {
			t.Errorf("expected the validation table to be rolled back, found %d", count)
		}

		var schemas int
		row = db.QueryRowContext(ctx, `SELECT count(*) FROM pg_namespace WHERE nspname = 'joka_consolidate_check'`)
		if err := row.Scan(&schemas); err != nil {
			t.Fatalf("counting leftover schemas: %v", err)
		}
		if schemas != 0 {
			t.Errorf("expected the validation schema to be rolled back, found %d", schemas)
		}
	})

	t.Run("it rejects SQL that does not apply", func(t *testing.T) {
		// Exactly the shape the bug report hit: an unterminated CREATE TABLE
		// followed by a CREATE INDEX.
		err := adapter.ValidateSchemaSQL(ctx, "CREATE TABLE test_pg_validate_bad (id int)\nCREATE INDEX i ON test_pg_validate_bad (id);")
		if !errors.Is(err, domain.ErrSchemaNotApplicable) {
			t.Fatalf("expected ErrSchemaNotApplicable, got: %v", err)
		}
	})

	t.Run("it does not resolve references against the real schema", func(t *testing.T) {
		t.Cleanup(func() { testlib.DropTablePostgres(t, db, "test_pg_validate_real") })
		execPG(t, db, `CREATE TABLE test_pg_validate_real (id int PRIMARY KEY)`)

		// The referenced table exists in the live schema but not in the
		// generated SQL — validation must still fail.
		err := adapter.ValidateSchemaSQL(ctx,
			"CREATE TABLE test_pg_validate_ref (id int PRIMARY KEY, other int REFERENCES test_pg_validate_real(id));")
		if !errors.Is(err, domain.ErrSchemaNotApplicable) {
			t.Fatalf("expected ErrSchemaNotApplicable, got: %v", err)
		}
	})
}

func TestPostgresRemoveMigrationRecords(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}
	ctx := context.Background()

	t.Cleanup(func() {
		testlib.DropTablePostgres(t, db, "joka_migrations")
		testlib.DropTablePostgres(t, db, "joka_snapshots")
	})

	adapter := infra.NewPostgresDBAdapter(db)
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
