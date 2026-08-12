package infra_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/domains/migration/infra"
	"github.com/apsdsm/joka/testlib"
)

// TestPgSchemaDumper covers the whole point of the dump-based baseline: the
// objects joka's own reconstruction could never carry (enum types, views,
// functions, identity, arrays) survive a dump and rebuild an empty database.
func TestPgSchemaDumper(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	if _, err := exec.LookPath("pg_dump"); err != nil {
		t.Skip("pg_dump not installed")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}
	dsn, err := testlib.GetTestPostgresDSN()
	if err != nil {
		t.Fatalf("getting test dsn: %v", err)
	}
	ctx := context.Background()

	t.Cleanup(func() {
		execPG(t, db, `DROP VIEW IF EXISTS test_dump_view`)
		testlib.DropTablePostgres(t, db, "test_dump_child")
		testlib.DropTablePostgres(t, db, "test_dump_parent")
		execPG(t, db, `DROP FUNCTION IF EXISTS test_dump_fn()`)
		execPG(t, db, `DROP TYPE IF EXISTS test_dump_mood`)
		testlib.DropTablePostgres(t, db, "joka_migrations")
	})

	// A joka table, to prove it is excluded along with its owned sequence.
	execPG(t, db, `CREATE TABLE joka_migrations (id SERIAL PRIMARY KEY, migration_index VARCHAR(255) NOT NULL UNIQUE)`)
	execPG(t, db, `CREATE TYPE test_dump_mood AS ENUM ('ok', 'bad')`)
	execPG(t, db, `CREATE TABLE test_dump_parent (
		id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		tags text[],
		m test_dump_mood,
		amt numeric(10,2)
	)`)
	execPG(t, db, `CREATE INDEX test_dump_tags_idx ON test_dump_parent USING gin (tags)`)
	execPG(t, db, `CREATE TABLE test_dump_child (id serial PRIMARY KEY, parent_id bigint REFERENCES test_dump_parent(id))`)
	execPG(t, db, `CREATE VIEW test_dump_view AS SELECT id, m FROM test_dump_parent`)
	execPG(t, db, `CREATE FUNCTION test_dump_fn() RETURNS int AS $$ BEGIN RETURN 1; END; $$ LANGUAGE plpgsql`)

	dumper, err := infra.NewSchemaDumper(jokadb.Postgres, db, dsn)
	if err != nil {
		t.Fatalf("NewSchemaDumper: %v", err)
	}

	script, err := dumper.Dump(ctx)
	if err != nil {
		t.Fatalf("Dump: %v", err)
	}

	t.Run("it strips psql meta-commands", func(t *testing.T) {
		for _, line := range strings.Split(script, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), `\`) {
				t.Fatalf("psql meta-command survived: %q", line)
			}
		}
	})

	t.Run("it excludes joka tracking tables and their sequences", func(t *testing.T) {
		if strings.Contains(script, "joka_") {
			for _, line := range strings.Split(script, "\n") {
				if strings.Contains(line, "joka_") {
					t.Errorf("joka reference survived: %q", line)
				}
			}
		}
	})

	t.Run("it carries what a table snapshot cannot", func(t *testing.T) {
		for _, want := range []string{
			"CREATE TYPE public.test_dump_mood AS ENUM",
			"CREATE VIEW public.test_dump_view",
			"CREATE FUNCTION public.test_dump_fn()",
			"GENERATED ALWAYS AS IDENTITY",
			"tags text[]",
			"amt numeric(10,2)",
			"CREATE INDEX test_dump_tags_idx",
		} {
			if !strings.Contains(script, want) {
				t.Errorf("expected the dump to contain %q", want)
			}
		}
	})

	t.Run("the dump rebuilds the schema in an empty database", func(t *testing.T) {
		if err := testlib.ApplyToScratchPostgresDB(t, script, "joka_dump_roundtrip"); err != nil {
			t.Fatalf("dump did not apply:\n%v", err)
		}
	})
}
