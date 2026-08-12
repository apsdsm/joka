package migration

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	jokadb "github.com/apsdsm/joka/db"
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

func setupConsolidateDB(t *testing.T) (*sql.DB, string) {
	t.Helper()

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}
	ctx := context.Background()

	t.Cleanup(func() {
		testlib.DropTablePostgres(t, db, "joka_migrations")
		testlib.DropTablePostgres(t, db, "joka_snapshots")
	})

	if err := infra.NewPostgresDBAdapter(db).CreateMigrationsTable(ctx); err != nil {
		t.Fatalf("CreateMigrationsTable: %v", err)
	}
	return db, t.TempDir()
}

func TestConsolidateReconcilesBookkeeping(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, dir := setupConsolidateDB(t)
	ctx := context.Background()

	t.Cleanup(func() {
		testlib.DropTablePostgres(t, db, "test_cons_orders")
		testlib.DropTablePostgres(t, db, "test_cons_users")
	})

	applyMigration(t, db, dir, "240101000000_users.sql",
		`CREATE TABLE test_cons_users (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, tags text[], email varchar(255) NOT NULL);`)
	applyMigration(t, db, dir, "240102000000_orders.sql",
		`CREATE TABLE test_cons_orders (id serial PRIMARY KEY, user_id bigint REFERENCES test_cons_users(id), total numeric(10,2));
		 CREATE INDEX test_cons_orders_user_idx ON test_cons_orders (user_id);`)

	err := RunConsolidateCommand{
		DB:            db,
		Driver:        jokadb.Postgres,
		MigrationsDir: dir,
		UpToIndex:     "240102000000",
		AutoConfirm:   true,
	}.Execute(ctx)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

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

	t.Run("the consolidated file rebuilds the schema it replaced", func(t *testing.T) {
		content, err := os.ReadFile(filepath.Join(dir, "240102000000_consolidated.sql"))
		if err != nil {
			t.Fatalf("reading consolidated file: %v", err)
		}
		if err := infra.NewPostgresDBAdapter(db).ValidateSchemaSQL(ctx, string(content)); err != nil {
			t.Fatalf("consolidated file does not apply:\n%v\n\nfile:\n%s", err, content)
		}
	})
}

func TestConsolidateRefusesUncapturableSchema(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, dir := setupConsolidateDB(t)
	ctx := context.Background()

	t.Cleanup(func() {
		if _, err := db.ExecContext(ctx, "DROP VIEW IF EXISTS test_cons_view"); err != nil {
			t.Logf("dropping view: %v", err)
		}
		testlib.DropTablePostgres(t, db, "test_cons_base")
	})

	applyMigration(t, db, dir, "240101000000_base.sql",
		`CREATE TABLE test_cons_base (id int PRIMARY KEY, name text);`)
	applyMigration(t, db, dir, "240102000000_view.sql",
		`CREATE VIEW test_cons_view AS SELECT id FROM test_cons_base;`)

	cmd := RunConsolidateCommand{
		DB:            db,
		Driver:        jokadb.Postgres,
		MigrationsDir: dir,
		UpToIndex:     "240102000000",
		AutoConfirm:   true,
	}

	t.Run("it refuses rather than dropping the view from the baseline", func(t *testing.T) {
		err := cmd.Execute(ctx)
		if !errors.Is(err, domain.ErrUnsupportedSchemaObjects) {
			t.Fatalf("expected ErrUnsupportedSchemaObjects, got: %v", err)
		}
	})

	t.Run("it leaves the migration files and records untouched", func(t *testing.T) {
		files, err := infra.ListMigrationFiles(dir)
		if err != nil {
			t.Fatalf("ListMigrationFiles: %v", err)
		}
		if len(files) != 2 {
			t.Fatalf("expected both migration files to survive the refusal, got %+v", files)
		}

		applied, err := infra.NewPostgresDBAdapter(db).GetAppliedMigrations(ctx)
		if err != nil {
			t.Fatalf("GetAppliedMigrations: %v", err)
		}
		if len(applied) != 2 {
			t.Fatalf("expected both migration records to survive the refusal, got %+v", applied)
		}
	})

	t.Run("it proceeds when the caller opts in with --allow-unsupported", func(t *testing.T) {
		cmd.AllowUnsupported = true
		if err := cmd.Execute(ctx); err != nil {
			t.Fatalf("Execute with AllowUnsupported: %v", err)
		}

		files, err := infra.ListMigrationFiles(dir)
		if err != nil {
			t.Fatalf("ListMigrationFiles: %v", err)
		}
		if len(files) != 1 {
			t.Fatalf("expected a single consolidated file, got %+v", files)
		}
	})
}
