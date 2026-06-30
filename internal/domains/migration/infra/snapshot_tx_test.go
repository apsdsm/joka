package infra_test

import (
	"context"
	"strings"
	"testing"

	"github.com/apsdsm/joka/internal/domains/migration/infra"
	"github.com/apsdsm/joka/testlib"
)

// TestPostgresSnapshotRunsInMigrationTx is a regression test for the migrate-up
// deadlock: CaptureSchemaSnapshot must read the schema through the migration
// transaction (p.db), not a separate pool connection (p.conn). If it used the
// pool, a snapshot taken while the migration tx holds ACCESS EXCLUSIVE on a
// table it just ALTER'd/CREATE'd would block on that lock — an unbreakable
// cross-connection deadlock — and would also miss tables created in the
// still-open tx.
//
// We assert the snapshot SEES a table created inside the same uncommitted tx.
// That can only be true when the snapshot reads on the tx connection, which is
// exactly the connection that holds the locks — so it cannot self-deadlock.
func TestPostgresSnapshotRunsInMigrationTx(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Cleanup(func() {
		testlib.DropTablePostgres(t, db, "gadget")
		testlib.DropTablePostgres(t, db, "joka_snapshots")
	})

	ctx := context.Background()

	// Snapshots table exists up front (created on the pool, committed).
	pool := infra.NewPostgresDBAdapter(db)
	if err := pool.EnsureSnapshotsTable(ctx); err != nil {
		t.Fatalf("EnsureSnapshotsTable: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer tx.Rollback()

	// Create a table inside the tx — uncommitted, so a separate pool connection
	// cannot see it.
	if _, err := tx.ExecContext(ctx, `CREATE TABLE gadget (id integer NOT NULL)`); err != nil {
		t.Fatalf("create gadget in tx: %v", err)
	}

	txAdapter := infra.NewPostgresTxDBAdapter(tx, db)
	if err := txAdapter.CaptureSchemaSnapshot(ctx, "test-snapshot-001"); err != nil {
		t.Fatalf("CaptureSchemaSnapshot in tx: %v", err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	snapshot, err := pool.GetSchemaSnapshot(ctx, "test-snapshot-001")
	if err != nil {
		t.Fatalf("GetSchemaSnapshot: %v", err)
	}

	if !strings.Contains(snapshot, "gadget") {
		t.Fatalf("snapshot taken in-tx did not include the table created in the same tx; "+
			"it must read through the tx connection, not the pool. snapshot: %s", snapshot)
	}
}
