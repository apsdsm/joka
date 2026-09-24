package jokalib_test

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	joka "github.com/apsdsm/joka/jokalib"
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
			"joka_migrations", "joka_snapshots", "joka_state", "joka_meta",
			"lib_widgets",
		} {
			testlib.DropTablePostgres(t, db, table)
		}
	}
	drop()
	t.Cleanup(drop)

	return db
}

func writeMigration(t *testing.T, dir, name, body string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

func TestMigrateUp(t *testing.T) {
	ctx := context.Background()

	t.Run("it applies pending migrations", func(t *testing.T) {
		db := freshDB(t)
		dir := t.TempDir()
		writeMigration(t, dir, "250101000000_widgets.sql",
			"CREATE TABLE lib_widgets (id SERIAL PRIMARY KEY, name TEXT);")

		if err := joka.Init(ctx, db); err != nil {
			t.Fatalf("Init: %v", err)
		}
		if err := joka.MigrateUp(ctx, db, dir); err != nil {
			t.Fatalf("MigrateUp: %v", err)
		}

		var n int
		if err := db.QueryRow(
			`SELECT count(*) FROM information_schema.tables
			 WHERE table_schema = 'public' AND table_name = 'lib_widgets'`).Scan(&n); err != nil {
			t.Fatalf("checking the table: %v", err)
		}
		if n != 1 {
			t.Error("expected MigrateUp to have created lib_widgets")
		}
	})

	t.Run("it splits statements the way the tool does", func(t *testing.T) {
		// This is why the package exists. jjc2's test helpers wrote their own
		// splitter, it broke on a semicolon inside a comment, and the team
		// wrote down a rule ("no ; in migration comments") that described
		// their splitter rather than joka's. A migration joka accepts must go
		// through the library unchanged.
		db := freshDB(t)
		dir := t.TempDir()
		writeMigration(t, dir, "250101000000_widgets.sql", `
-- a comment containing a semicolon; like this one
CREATE TABLE lib_widgets (id SERIAL PRIMARY KEY, name TEXT);
INSERT INTO lib_widgets (name) VALUES ('one');
INSERT INTO lib_widgets (name) VALUES ('two');
`)

		if err := joka.Init(ctx, db); err != nil {
			t.Fatalf("Init: %v", err)
		}
		if err := joka.MigrateUp(ctx, db, dir); err != nil {
			t.Fatalf("MigrateUp: %v", err)
		}

		var rows int
		if err := db.QueryRow(`SELECT count(*) FROM lib_widgets`).Scan(&rows); err != nil {
			t.Fatalf("counting rows: %v", err)
		}
		if rows != 2 {
			t.Errorf("expected both inserts to have applied, got %d rows", rows)
		}
	})

	t.Run("it is silent by default and writes to WithOutput when asked", func(t *testing.T) {
		// A package a test helper calls must not write to the process stdout.
		db := freshDB(t)
		dir := t.TempDir()
		writeMigration(t, dir, "250101000000_widgets.sql",
			"CREATE TABLE lib_widgets (id SERIAL PRIMARY KEY);")

		stdout := captureStdout(t, func() {
			if err := joka.Init(ctx, db); err != nil {
				t.Errorf("Init: %v", err)
			}
			if err := joka.MigrateUp(ctx, db, dir); err != nil {
				t.Errorf("MigrateUp: %v", err)
			}
		})
		if stdout != "" {
			t.Errorf("expected nothing on stdout, got %q", stdout)
		}

		var buf bytes.Buffer
		if err := joka.MigrateUp(ctx, db, dir, joka.WithOutput(&buf)); err != nil {
			t.Fatalf("MigrateUp: %v", err)
		}
		if !strings.Contains(buf.String(), "No pending migrations") {
			t.Errorf("expected progress in the writer, got %q", buf.String())
		}
	})

	t.Run("a missing directory is an error, not a silent no-op", func(t *testing.T) {
		db := freshDB(t)
		if err := joka.MigrateUp(ctx, db, filepath.Join(t.TempDir(), "nope")); err == nil {
			t.Error("expected an error for a directory that is not there")
		}
	})
}

// captureStdout runs fn with os.Stdout redirected and returns what was written.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("creating pipe: %v", err)
	}

	orig := os.Stdout
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = orig

	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("reading the pipe: %v", err)
	}
	r.Close()

	return buf.String()
}

func TestEntitySync(t *testing.T) {
	ctx := context.Background()
	db := freshDB(t)

	migrations := t.TempDir()
	writeMigration(t, migrations, "250101000000_widgets.sql",
		"CREATE TABLE lib_widgets (id SERIAL PRIMARY KEY, name TEXT NOT NULL UNIQUE);")

	if err := joka.Init(ctx, db); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := joka.MigrateUp(ctx, db, migrations); err != nil {
		t.Fatalf("MigrateUp: %v", err)
	}

	entities := t.TempDir()
	if err := os.WriteFile(filepath.Join(entities, "w.yaml"), []byte(
		"entities:\n  - _is: lib_widgets\n    _id: w1\n    name: first\n"), 0644); err != nil {
		t.Fatalf("writing the seed file: %v", err)
	}

	t.Run("it seeds the declared rows", func(t *testing.T) {
		if err := joka.EntitySync(ctx, db, entities); err != nil {
			t.Fatalf("EntitySync: %v", err)
		}

		var name string
		if err := db.QueryRow(`SELECT name FROM lib_widgets`).Scan(&name); err != nil {
			t.Fatalf("reading the row: %v", err)
		}
		if name != "first" {
			t.Errorf("expected the seeded row, got %q", name)
		}
	})

	t.Run("a second run changes nothing", func(t *testing.T) {
		// The property a test helper depends on: calling it twice in one suite
		// must not insert a second copy of every row.
		if err := joka.EntitySync(ctx, db, entities); err != nil {
			t.Fatalf("EntitySync: %v", err)
		}

		var rows int
		if err := db.QueryRow(`SELECT count(*) FROM lib_widgets`).Scan(&rows); err != nil {
			t.Fatalf("counting rows: %v", err)
		}
		if rows != 1 {
			t.Errorf("expected one row after two syncs, got %d", rows)
		}
	})
}
