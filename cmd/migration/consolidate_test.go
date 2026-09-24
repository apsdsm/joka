package migration

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apsdsm/joka/internal/domains/migration/app"
	"github.com/apsdsm/joka/internal/domains/migration/domain"
	"github.com/apsdsm/joka/internal/domains/migration/infra"
	"github.com/apsdsm/joka/testlib"
)

// applyMigration writes a migration file, applies it, and records it the way
// `migrate up` does — enough setup to consolidate against.
func applyMigration(t *testing.T, db *sql.DB, dir, name, body string) {
	t.Helper()
	ctx := context.Background()

	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}

	adapter := infra.NewPostgresDBAdapter(db)
	if err := adapter.ApplySQLFromFile(ctx, path); err != nil {
		t.Fatalf("applying %s: %v", name, err)
	}
	index := strings.SplitN(name, "_", 2)[0]
	if err := adapter.RecordMigrationApplied(ctx, index); err != nil {
		t.Fatalf("recording %s: %v", name, err)
	}
	if err := adapter.CaptureSchemaSnapshot(ctx, index); err != nil {
		t.Fatalf("snapshotting %s: %v", name, err)
	}
}

func setupConsolidateDB(t *testing.T) (*sql.DB, string, string) {
	t.Helper()
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
		testlib.DropTablePostgres(t, db, "joka_migrations")
		testlib.DropTablePostgres(t, db, "joka_snapshots")
	})

	if err := infra.NewPostgresDBAdapter(db).CreateMigrationsTable(ctx); err != nil {
		t.Fatalf("CreateMigrationsTable: %v", err)
	}
	return db, dsn, t.TempDir()
}

// TestConsolidate covers the two failures from bug_report_20260807.md that live
// in the command rather than the schema reconstruction: the baseline has to be
// something a database can actually be rebuilt from, and the tracking tables
// have to be left consistent with the files on disk.
func TestConsolidate(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, dsn, dir := setupConsolidateDB(t)
	ctx := context.Background()

	t.Cleanup(func() {
		if _, err := db.ExecContext(ctx, "DROP VIEW IF EXISTS test_cons_summary"); err != nil {
			t.Logf("dropping view: %v", err)
		}
		testlib.DropTablePostgres(t, db, "test_cons_orders")
		testlib.DropTablePostgres(t, db, "test_cons_users")
		if _, err := db.ExecContext(ctx, "DROP TYPE IF EXISTS test_cons_state"); err != nil {
			t.Logf("dropping type: %v", err)
		}
	})

	// A schema the old snapshot-based consolidate could not represent: an enum
	// type, an identity column, an array, a view.
	applyMigration(t, db, dir, "240101000000_users.sql",
		`CREATE TYPE test_cons_state AS ENUM ('active', 'closed');
		 CREATE TABLE test_cons_users (
		   id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		   tags text[],
		   state test_cons_state NOT NULL DEFAULT 'active',
		   email varchar(255) NOT NULL
		 );`)
	applyMigration(t, db, dir, "240102000000_orders.sql",
		`CREATE TABLE test_cons_orders (id serial PRIMARY KEY, user_id bigint REFERENCES test_cons_users(id), total numeric(10,2));
		 CREATE INDEX test_cons_orders_user_idx ON test_cons_orders (user_id);
		 CREATE VIEW test_cons_summary AS SELECT id, email FROM test_cons_users;`)

	err := RunConsolidateCommand{
		DB:            db,
		DSN:           dsn,
		MigrationsDir: dir,
		UpToIndex:     "240102000000",
		AutoConfirm:   true,
	}.Execute(ctx)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	baselinePath := filepath.Join(dir, "240102000000_consolidated.sql")

	t.Run("it leaves exactly the consolidated file on disk", func(t *testing.T) {
		files, err := infra.ListMigrationFiles(dir)
		if err != nil {
			t.Fatalf("ListMigrationFiles: %v", err)
		}
		if len(files) != 1 || files[0].Index != "240102000000" {
			t.Fatalf("expected only 240102000000_consolidated.sql, got %+v", files)
		}
	})

	t.Run("it removes the migration records the file no longer covers", func(t *testing.T) {
		applied, err := infra.NewPostgresDBAdapter(db).GetAppliedMigrations(ctx)
		if err != nil {
			t.Fatalf("GetAppliedMigrations: %v", err)
		}
		if len(applied) != 1 || applied[0].MigrationIndex != "240102000000" {
			t.Fatalf("expected only the target record to remain, got %+v", applied)
		}
	})

	t.Run("the database can still migrate afterwards", func(t *testing.T) {
		chain, err := app.GetMigrationChainAction{
			DB:            infra.NewPostgresDBAdapter(db),
			MigrationsDir: dir,
		}.Execute(ctx)
		if err != nil {
			t.Fatalf("migration chain broken after consolidation: %v", err)
		}
		if len(chain) != 1 || chain[0].Status != domain.StatusApplied {
			t.Fatalf("expected one applied migration, got %+v", chain)
		}
	})

	t.Run("the baseline carries the objects a table snapshot could not", func(t *testing.T) {
		content, err := os.ReadFile(baselinePath)
		if err != nil {
			t.Fatalf("reading consolidated file: %v", err)
		}
		baseline := string(content)

		if !strings.Contains(baseline, "using pg_dump") {
			t.Errorf("expected the header to name the dump tool, got:\n%s", firstLines(baseline, 4))
		}
		for _, want := range []string{
			"CREATE TYPE public.test_cons_state AS ENUM",
			"CREATE VIEW public.test_cons_summary",
			"GENERATED ALWAYS AS IDENTITY",
			"tags text[]",
		} {
			if !strings.Contains(baseline, want) {
				t.Errorf("expected the baseline to contain %q", want)
			}
		}
		if strings.Contains(baseline, "joka_migrations") {
			t.Error("expected joka tracking tables to be excluded from the baseline")
		}
	})

	t.Run("the baseline rebuilds the schema from nothing", func(t *testing.T) {
		content, err := os.ReadFile(baselinePath)
		if err != nil {
			t.Fatalf("reading consolidated file: %v", err)
		}
		if err := testlib.ApplyToScratchPostgresDB(t, string(content), "joka_baseline_replay"); err != nil {
			t.Fatalf("baseline does not apply:\n%v", err)
		}
	})
}

func TestConsolidateRequiresTheLastAppliedMigration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, dsn, dir := setupConsolidateDB(t)
	ctx := context.Background()

	t.Cleanup(func() {
		testlib.DropTablePostgres(t, db, "test_cons_late")
		testlib.DropTablePostgres(t, db, "test_cons_early")
	})

	applyMigration(t, db, dir, "240101000000_early.sql", `CREATE TABLE test_cons_early (id int PRIMARY KEY);`)
	applyMigration(t, db, dir, "240102000000_mid.sql", `ALTER TABLE test_cons_early ADD COLUMN name text;`)
	applyMigration(t, db, dir, "240103000000_late.sql", `CREATE TABLE test_cons_late (id int PRIMARY KEY);`)

	// A dump describes the schema as it is now, which includes 240103000000.
	// Writing that as the 240102000000 baseline would be a lie.
	err := RunConsolidateCommand{
		DB:            db,
		DSN:           dsn,
		MigrationsDir: dir,
		UpToIndex:     "240102000000",
		AutoConfirm:   true,
	}.Execute(ctx)

	if !errors.Is(err, domain.ErrNotLastApplied) {
		t.Fatalf("expected ErrNotLastApplied, got: %v", err)
	}

	files, err := infra.ListMigrationFiles(dir)
	if err != nil {
		t.Fatalf("ListMigrationFiles: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("expected all three migration files to survive the refusal, got %+v", files)
	}

	applied, err := infra.NewPostgresDBAdapter(db).GetAppliedMigrations(ctx)
	if err != nil {
		t.Fatalf("GetAppliedMigrations: %v", err)
	}
	if len(applied) != 3 {
		t.Fatalf("expected all three records to survive the refusal, got %+v", applied)
	}
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
