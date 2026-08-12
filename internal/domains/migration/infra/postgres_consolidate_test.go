package infra_test

import (
	"context"
	"testing"

	"github.com/apsdsm/joka/internal/domains/migration/infra"
	"github.com/apsdsm/joka/testlib"
)

// TestPostgresRemoveMigrationRecords covers the bookkeeping half of consolidate:
// the records a consolidated baseline replaces have to leave the tracking tables,
// or the chain (zipped positionally against files on disk) breaks on the next
// command.
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
