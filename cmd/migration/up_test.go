package migration

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apsdsm/joka/cmd/shared"
	"github.com/apsdsm/joka/internal/domains/migration/infra"
	"github.com/apsdsm/joka/testlib"
)

// TestDecliningMigrateUpIsAnError guards the exit code of a declined migration.
//
// `joka migrate up && joka entity sync` carried on into the sync after the
// migration was declined, because declining returned nil, and the sync then ran
// against a schema that had not been migrated.
func TestDecliningMigrateUpIsAnError(t *testing.T) {
	ctx := context.Background()

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Skipf("PostgreSQL test container unavailable: %v", err)
	}

	testlib.DropTablePostgres(t, db, "joka_migrations")
	testlib.DropTablePostgres(t, db, "joka_snapshots")
	testlib.DropTablePostgres(t, db, "test_declined_users")
	t.Cleanup(func() {
		testlib.DropTablePostgres(t, db, "joka_migrations")
		testlib.DropTablePostgres(t, db, "joka_snapshots")
		testlib.DropTablePostgres(t, db, "test_declined_users")
	})

	if err := (RunInitCommand{DB: db, OutputFormat: "text"}).Execute(ctx); err != nil {
		t.Fatalf("init: %v", err)
	}

	dir := t.TempDir()
	body := "CREATE TABLE test_declined_users (id SERIAL PRIMARY KEY);"
	if err := os.WriteFile(filepath.Join(dir, "250101000000_create.sql"), []byte(body), 0644); err != nil {
		t.Fatalf("writing the migration: %v", err)
	}

	// Answer the confirmation with anything but "yes".
	original := shared.Stdin
	shared.Stdin = bufio.NewReader(strings.NewReader("no\n"))
	t.Cleanup(func() { shared.Stdin = original })

	err = RunMigrateUpCommand{
		DB:            db,
		MigrationsDir: dir,
		AutoConfirm:   false,
		OutputFormat:  "text",
		SkipLock:      true,
	}.Execute(ctx)

	if !errors.Is(err, shared.ErrCancelled) {
		t.Fatalf("expected shared.ErrCancelled when the operator declines, got: %v", err)
	}

	// Declining must also mean nothing was applied.
	adapter := infra.NewPostgresDBAdapter(db)
	applied, err := adapter.GetAppliedMigrations(ctx)
	if err != nil {
		t.Fatalf("reading applied migrations: %v", err)
	}
	if len(applied) != 0 {
		t.Errorf("expected no migrations applied after declining, got %d", len(applied))
	}
}
