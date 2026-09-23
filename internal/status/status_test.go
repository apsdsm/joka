package status_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/status"
	"github.com/apsdsm/joka/testlib"
)

// bareDB returns a database with none of joka's tables, and drops them again
// afterwards so a later test does not inherit whatever this one found.
func bareDB(t *testing.T) *sql.DB {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	drop := func() {
		for _, table := range []string{"joka_meta", "joka_lock", "joka_state", "joka_snapshots", "joka_migrations"} {
			testlib.DropTablePostgres(t, db, table)
		}
	}
	drop()
	t.Cleanup(drop)

	return db
}

// inputs points status at an empty entities directory so the entity half has
// something readable to report.
func inputs(t *testing.T, db *sql.DB) status.Inputs {
	t.Helper()

	dir := t.TempDir()
	return status.Inputs{
		DB:            db,
		MigrationsDir: filepath.Join(dir, "migrations"),
		EntitiesDir:   filepath.Join(dir, "entities"),
		StateFile:     filepath.Join(dir, "joka.state.json"),
	}
}

func TestStatusCreatesNothing(t *testing.T) {
	// Every other command auto-creates the table it needs. Status must not: a
	// missing table is a finding, not something to fix on the way past. Two of
	// the reads would create one if called directly — GetLatestSnapshotIndex
	// ensures joka_snapshots, and the lock adapter's GetLock ensures joka_lock.
	db := bareDB(t)
	ctx := context.Background()

	if _, err := status.Build(ctx, inputs(t, db)); err != nil {
		t.Fatalf("Build against a bare database: %v", err)
	}

	for _, table := range []string{"joka_meta", "joka_lock", "joka_state", "joka_snapshots", "joka_migrations"} {
		exists, err := jokadb.TableExists(ctx, db, table)
		if err != nil {
			t.Fatalf("checking %s: %v", table, err)
		}
		if exists {
			t.Errorf("status created %s", table)
		}
	}
}

func TestStatusReportsAnUninitialisedDatabase(t *testing.T) {
	db := bareDB(t)

	report, err := status.Build(context.Background(), inputs(t, db))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if report.Migrations.Tracked {
		t.Error("expected joka_migrations reported as absent")
	}
	if report.Meta.Present {
		t.Error("expected no bookkeeping reported")
	}
	if report.Lock != nil {
		t.Errorf("expected no lock, got %+v", report.Lock)
	}
}

func TestStatusReportsTheStateAuditVerdict(t *testing.T) {
	// The reason status exists. AuditState computes seven verdicts and only one
	// of them was ever acted on; the other six were discarded, and Describe()
	// had no caller outside the test suite.
	db := bareDB(t)
	in := inputs(t, db)

	// A state file describing a database that holds no state of its own. This
	// is what `joka drop` leaves behind.
	if err := os.WriteFile(in.StateFile,
		[]byte(`{"identity":"deadbeef","version":4,"written_by":"0.14.0","state":{"version":2}}`),
		0o600); err != nil {
		t.Fatalf("writing the state file: %v", err)
	}

	report, err := status.Build(context.Background(), in)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if report.StateFile.Verdict != "database_untracked" {
		t.Errorf("expected database_untracked, got %q", report.StateFile.Verdict)
	}
	if !report.StateFile.NeedsAttention {
		t.Error("expected it flagged")
	}
	if report.StateFile.Note == report.StateFile.Verdict {
		t.Error("expected a sentence rather than the bare verdict")
	}
}

func TestStatusReportsAHeldLock(t *testing.T) {
	// Before this the only way to learn a lock was held was to try a mutating
	// command and be refused, or to run `joka unlock`, which releases it.
	db := bareDB(t)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, `
		CREATE TABLE joka_lock (
			id INTEGER PRIMARY KEY DEFAULT 1,
			locked_by VARCHAR(255) NOT NULL,
			locked_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			operation VARCHAR(255) NOT NULL
		)`); err != nil {
		t.Fatalf("creating the lock table: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO joka_lock (id, locked_by, operation) VALUES (1, 'host:42', 'entity sync')`); err != nil {
		t.Fatalf("recording a holder: %v", err)
	}

	report, err := status.Build(ctx, inputs(t, db))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if report.Lock == nil {
		t.Fatal("expected the held lock reported")
	}
	if report.Lock.LockedBy != "host:42" || report.Lock.Operation != "entity sync" {
		t.Errorf("expected the holder named, got %+v", report.Lock)
	}
}

func TestStatusSurvivesAnUnreadableHalf(t *testing.T) {
	// The reason to run status is usually that something is wrong, so one
	// unreadable section must not take the other with it.
	db := bareDB(t)

	in := inputs(t, db)
	in.EntitiesDir = filepath.Join(t.TempDir(), "does-not-exist")

	report, err := status.Build(context.Background(), in)
	if err != nil {
		t.Fatalf("expected a report despite the missing directory, got %v", err)
	}
	if report.Entities.Problem == "" {
		t.Error("expected the entity half to report why it could not be read")
	}
	if report.Migrations.Tracked {
		t.Error("expected the migration half still assembled")
	}
}
