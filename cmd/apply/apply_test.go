package apply_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apsdsm/joka/cmd/apply"
	"github.com/apsdsm/joka/cmd/shared"
	"github.com/apsdsm/joka/testlib"
)

func freshDB(t *testing.T) *sql.DB {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	drop := func() {
		for _, table := range []string{
			"joka_migrations", "joka_snapshots", "joka_state", "joka_meta", "joka_lock",
			"apply_widgets",
		} {
			testlib.DropTablePostgres(t, db, table)
		}
	}
	drop()
	t.Cleanup(drop)

	return db
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

// root builds a joka root with one migration and one seed file.
func root(t *testing.T) (migrations, entities string) {
	t.Helper()

	base := t.TempDir()
	migrations = filepath.Join(base, "migrations")
	entities = filepath.Join(base, "entities")

	write(t, migrations, "250101000000_init.sql",
		"CREATE TABLE apply_widgets (id SERIAL PRIMARY KEY, name TEXT NOT NULL UNIQUE);")
	write(t, entities, "w.yaml",
		"entities:\n  - _is: apply_widgets\n    _id: w1\n    name: First\n")

	return migrations, entities
}

func command(db *sql.DB, migrations, entities string) apply.RunApplyCommand {
	return apply.RunApplyCommand{
		DB:            db,
		MigrationsDir: migrations,
		EntitiesDirs:  []string{entities},
		AutoConfirm:   true,
		OutputFormat:  "text",
		StateFile:     filepath.Join(entities, "..", "joka.state.json"),
	}
}

func TestApply(t *testing.T) {
	ctx := context.Background()

	t.Run("it migrates and seeds in one run", func(t *testing.T) {
		db := freshDB(t)
		migrations, entities := root(t)

		if err := command(db, migrations, entities).Execute(ctx); err != nil {
			t.Fatalf("Execute: %v", err)
		}

		var name string
		if err := db.QueryRow(`SELECT name FROM apply_widgets`).Scan(&name); err != nil {
			t.Fatalf("reading the seeded row: %v", err)
		}
		if name != "First" {
			t.Errorf("expected the seeded row, got %q", name)
		}
	})

	t.Run("a second run has nothing to do", func(t *testing.T) {
		db := freshDB(t)
		migrations, entities := root(t)

		if err := command(db, migrations, entities).Execute(ctx); err != nil {
			t.Fatalf("first Execute: %v", err)
		}
		if err := command(db, migrations, entities).Execute(ctx); err != nil {
			t.Fatalf("second Execute: %v", err)
		}

		var rows int
		if err := db.QueryRow(`SELECT count(*) FROM apply_widgets`).Scan(&rows); err != nil {
			t.Fatalf("counting: %v", err)
		}
		if rows != 1 {
			t.Errorf("expected one row after two applies, got %d", rows)
		}
	})

	t.Run("a dry run applies nothing and reports work pending", func(t *testing.T) {
		db := freshDB(t)
		migrations, entities := root(t)

		cmd := command(db, migrations, entities)
		cmd.DryRun = true

		err := cmd.Execute(ctx)
		if !errors.Is(err, shared.ErrChangesPending) {
			t.Fatalf("expected pending work to be reported, got: %v", err)
		}

		// The speculation must leave nothing behind: the table the migration
		// creates is rolled back with it.
		var exists int
		db.QueryRow(`SELECT count(*) FROM information_schema.tables
		             WHERE table_schema='public' AND table_name='apply_widgets'`).Scan(&exists)
		if exists != 0 {
			t.Error("expected the speculative migration to have been rolled back")
		}
	})

	t.Run("a dry run with nothing to do reports nothing pending", func(t *testing.T) {
		db := freshDB(t)
		migrations, entities := root(t)

		if err := command(db, migrations, entities).Execute(ctx); err != nil {
			t.Fatalf("Execute: %v", err)
		}

		cmd := command(db, migrations, entities)
		cmd.DryRun = true
		if err := cmd.Execute(ctx); err != nil {
			t.Errorf("expected a clean dry run to succeed, got: %v", err)
		}
	})

	t.Run("it plans seeds against the schema the migrations will leave", func(t *testing.T) {
		// The reason apply speculates at all. `entity sync --dry-run` cannot
		// do this: it reads the live row and the column is not there yet.
		db := freshDB(t)
		migrations, entities := root(t)

		if err := command(db, migrations, entities).Execute(ctx); err != nil {
			t.Fatalf("first Execute: %v", err)
		}

		write(t, migrations, "250202000000_add_tier.sql",
			"ALTER TABLE apply_widgets ADD COLUMN tier TEXT NOT NULL DEFAULT 'standard';")
		write(t, entities, "w.yaml",
			"entities:\n  - _is: apply_widgets\n    _id: w1\n    name: First\n    tier: premium\n")

		cmd := command(db, migrations, entities)
		cmd.DryRun = true
		if err := cmd.Execute(ctx); !errors.Is(err, shared.ErrChangesPending) {
			t.Fatalf("expected the plan to succeed and report work, got: %v", err)
		}

		if err := command(db, migrations, entities).Execute(ctx); err != nil {
			t.Fatalf("Execute: %v", err)
		}

		var tier string
		if err := db.QueryRow(`SELECT tier FROM apply_widgets`).Scan(&tier); err != nil {
			t.Fatalf("reading tier: %v", err)
		}
		if tier != "premium" {
			t.Errorf("expected the seed to have set the new column, got %q", tier)
		}
	})

	t.Run("a broken migration fails the plan without applying it", func(t *testing.T) {
		db := freshDB(t)
		migrations, entities := root(t)
		write(t, migrations, "250303000000_bad.sql", "THIS IS NOT SQL;")

		cmd := command(db, migrations, entities)
		cmd.DryRun = true

		err := cmd.Execute(ctx)
		if err == nil || !strings.Contains(err.Error(), "speculatively") {
			t.Fatalf("expected the speculative pass to report the failure, got: %v", err)
		}

		var exists int
		db.QueryRow(`SELECT count(*) FROM information_schema.tables
		             WHERE table_schema='public' AND table_name='apply_widgets'`).Scan(&exists)
		if exists != 0 {
			t.Error("expected a failed speculation to leave nothing behind")
		}
	})
}

func TestSpeculationWritesNoSnapshots(t *testing.T) {
	// Capturing a schema snapshot reconstructs every table from pg_catalog,
	// one query per table, after every migration. It is the most expensive
	// thing a migration run does, and the speculative pass was paying it for
	// snapshots it then rolled back — so `apply` cost it twice. Over a tunnel
	// to another region that was minutes of silence, reported as a hang.
	ctx := context.Background()
	db := freshDB(t)
	migrations, entities := root(t)

	cmd := command(db, migrations, entities)
	cmd.DryRun = true
	if err := cmd.Execute(ctx); !errors.Is(err, shared.ErrChangesPending) {
		t.Fatalf("expected work to be reported, got: %v", err)
	}

	// joka_snapshots may exist — the real pass creates it — but a dry run must
	// not have put a row in it.
	var rows int
	err := db.QueryRow(`SELECT count(*) FROM joka_snapshots`).Scan(&rows)
	if err != nil {
		// No table at all is the same answer, more strongly.
		return
	}
	if rows != 0 {
		t.Errorf("expected the speculative pass to capture no snapshots, got %d", rows)
	}
}

func TestTheRealPassStillCapturesSnapshots(t *testing.T) {
	// Skipping them while speculating must not skip them for real: they are
	// what `migrate verify` compares against.
	ctx := context.Background()
	db := freshDB(t)
	migrations, entities := root(t)

	if err := command(db, migrations, entities).Execute(ctx); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM joka_snapshots`).Scan(&rows); err != nil {
		t.Fatalf("reading joka_snapshots: %v", err)
	}
	if rows == 0 {
		t.Error("expected the real pass to have captured a snapshot")
	}
}
