package upgrade_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/meta"
	"github.com/apsdsm/joka/internal/upgrade"
	"github.com/apsdsm/joka/testlib"
)

const jokaVersion = "0.14.0-test"

// v1DB returns a database holding the shape joka wrote at tracking version 1:
// joka_entity_rows with a nullable, non-unique ref_id and no joka_meta.
func v1DB(t *testing.T) *sql.DB {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	drop := func() {
		testlib.DropTablePostgres(t, db, meta.Table)
		testlib.DropTablePostgres(t, db, "joka_entity_rows")
	}
	drop()
	t.Cleanup(drop)

	if _, err := db.Exec(`
		CREATE TABLE joka_entity_rows (
			id BIGSERIAL PRIMARY KEY,
			entity_file VARCHAR(512) NOT NULL,
			table_name VARCHAR(255) NOT NULL,
			row_pk BIGINT NOT NULL,
			pk_column VARCHAR(255) NOT NULL DEFAULT 'id',
			ref_id VARCHAR(255),
			insertion_order INT NOT NULL
		)`); err != nil {
		t.Fatalf("creating the v1 table: %v", err)
	}

	return db
}

func track(t *testing.T, db *sql.DB, file, refID string, pk int64) {
	t.Helper()

	var ref any
	if refID != "" {
		ref = refID
	}

	if _, err := db.Exec(
		`INSERT INTO joka_entity_rows (entity_file, table_name, row_pk, pk_column, ref_id, insertion_order)
		 VALUES ($1, 'fields', $2, 'id', $3, 0)`, file, pk, ref); err != nil {
		t.Fatalf("tracking a row: %v", err)
	}
}

func hasRefIDIndex(t *testing.T, db *sql.DB) bool {
	t.Helper()

	var n int
	if err := db.QueryRow(
		`SELECT count(*) FROM pg_indexes WHERE indexname = $1`, "joka_entity_rows_ref_id_key",
	).Scan(&n); err != nil {
		t.Fatalf("checking for the index: %v", err)
	}
	return n > 0
}

func TestRunUpgradesACleanV1Database(t *testing.T) {
	db := v1DB(t)
	ctx := context.Background()

	track(t, db, "a.yaml", "alpha", 1)
	track(t, db, "a.yaml", "beta", 2)

	applied, err := upgrade.Run(ctx, db, jokaVersion)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(applied) != 1 || applied[0].To != 2 {
		t.Fatalf("expected one step to version 2, got %+v", applied)
	}
	if !hasRefIDIndex(t, db) {
		t.Error("expected the unique index added")
	}

	state, err := meta.Read(ctx, db)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if state.TrackingVersion != meta.TrackingVersion {
		t.Errorf("expected the version stamped, got %d", state.TrackingVersion)
	}
}

func TestRunIsIdempotent(t *testing.T) {
	db := v1DB(t)
	ctx := context.Background()
	track(t, db, "a.yaml", "alpha", 1)

	if _, err := upgrade.Run(ctx, db, jokaVersion); err != nil {
		t.Fatalf("first Run: %v", err)
	}

	// Every mutating command runs this, so a database already current must not
	// re-apply anything.
	applied, err := upgrade.Run(ctx, db, jokaVersion)
	if err != nil {
		t.Fatalf("second Run: %v", err)
	}
	if len(applied) != 0 {
		t.Errorf("expected no steps on an up-to-date database, got %+v", applied)
	}
}

func TestRunOnADatabaseWithNoTrackingTable(t *testing.T) {
	db := v1DB(t)
	ctx := context.Background()
	testlib.DropTablePostgres(t, db, "joka_entity_rows")

	// Nothing has been synced here. The step has nothing to do, but the version
	// still moves so the next joka does not try again.
	if _, err := upgrade.Run(ctx, db, jokaVersion); err != nil {
		t.Fatalf("Run: %v", err)
	}

	state, err := meta.Read(ctx, db)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if state.TrackingVersion != meta.TrackingVersion {
		t.Errorf("expected the version stamped, got %d", state.TrackingVersion)
	}
}

func TestRunBlocksOnRowsWithNoRefID(t *testing.T) {
	db := v1DB(t)
	ctx := context.Background()

	track(t, db, "a.yaml", "alpha", 1)
	track(t, db, "old.yaml", "", 2)

	_, err := upgrade.Run(ctx, db, jokaVersion)
	if !errors.Is(err, upgrade.ErrBlocked) {
		t.Fatalf("expected ErrBlocked, got %v", err)
	}
	for _, want := range []string{"1 tracked row has no _id", "old.yaml", "entity reimport"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected %q in:\n%s", want, err)
		}
	}

	// A blocked upgrade must change nothing, or a retry starts from a state
	// nobody described.
	if hasRefIDIndex(t, db) {
		t.Error("expected no index after a blocked upgrade")
	}
	state, _ := meta.Read(ctx, db)
	if state.Present {
		t.Error("expected no version stamped after a blocked upgrade")
	}
}

func TestRunBlocksOnDuplicateRefID(t *testing.T) {
	db := v1DB(t)
	ctx := context.Background()

	// Two entity sets seeded into one database — version 1 allowed it because
	// it keyed on the file.
	track(t, db, "local/a.yaml", "role_owner", 1)
	track(t, db, "dev1/a.yaml", "role_owner", 2)

	_, err := upgrade.Run(ctx, db, jokaVersion)
	if !errors.Is(err, upgrade.ErrBlocked) {
		t.Fatalf("expected ErrBlocked, got %v", err)
	}
	for _, want := range []string{"role_owner", "local/a.yaml", "dev1/a.yaml", "entity forget"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected %q in:\n%s", want, err)
		}
	}
	if hasRefIDIndex(t, db) {
		t.Error("expected no index after a blocked upgrade")
	}
}

func TestRunProceedsOnceTheConflictIsCleared(t *testing.T) {
	db := v1DB(t)
	ctx := context.Background()

	track(t, db, "local/a.yaml", "role_owner", 1)
	track(t, db, "dev1/a.yaml", "role_owner", 2)

	if _, err := upgrade.Run(ctx, db, jokaVersion); !errors.Is(err, upgrade.ErrBlocked) {
		t.Fatalf("expected the upgrade blocked first, got %v", err)
	}

	if _, err := db.Exec(`DELETE FROM joka_entity_rows WHERE entity_file = 'dev1/a.yaml'`); err != nil {
		t.Fatalf("clearing the conflict: %v", err)
	}

	applied, err := upgrade.Run(ctx, db, jokaVersion)
	if err != nil {
		t.Fatalf("Run after clearing: %v", err)
	}
	if len(applied) != 1 {
		t.Errorf("expected the step to run, got %+v", applied)
	}
	if !hasRefIDIndex(t, db) {
		t.Error("expected the index added")
	}
}

func TestUpgradedIndexRejectsADuplicate(t *testing.T) {
	db := v1DB(t)
	ctx := context.Background()
	track(t, db, "a.yaml", "alpha", 1)

	if _, err := upgrade.Run(ctx, db, jokaVersion); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The constraint has to be real, not just checked once at upgrade time.
	_, err := db.Exec(
		`INSERT INTO joka_entity_rows (entity_file, table_name, row_pk, pk_column, ref_id, insertion_order)
		 VALUES ('b.yaml', 'fields', 2, 'id', 'alpha', 0)`)
	if err == nil {
		t.Error("expected the unique index to reject a second claim on alpha")
	}
}

func TestRunLeavesTrackedRowsAlone(t *testing.T) {
	db := v1DB(t)
	ctx := context.Background()

	track(t, db, "a.yaml", "alpha", 1)
	track(t, db, "a.yaml", "beta", 2)

	if _, err := upgrade.Run(ctx, db, jokaVersion); err != nil {
		t.Fatalf("Run: %v", err)
	}

	// An upgrade adds constraints; it never rewrites what a row means.
	var rows int
	if err := db.QueryRow(`SELECT count(*) FROM joka_entity_rows`).Scan(&rows); err != nil {
		t.Fatalf("counting: %v", err)
	}
	if rows != 2 {
		t.Errorf("expected both rows untouched, got %d", rows)
	}
}

func TestPreMarkerDatabaseIsNotMistakenForCurrent(t *testing.T) {
	db := v1DB(t)
	ctx := context.Background()

	// The bug this guards: defaulting an absent marker to the current version
	// makes every un-upgraded database look upgraded, so no upgrade ever runs.
	state, err := meta.Read(ctx, db)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if state.TrackingVersion != meta.PreMarkerVersion {
		t.Errorf("expected an absent marker to read as version %d, got %d",
			meta.PreMarkerVersion, state.TrackingVersion)
	}

	exists, err := jokadb.TableExists(ctx, db, meta.Table)
	if err != nil {
		t.Fatalf("TableExists: %v", err)
	}
	if exists {
		t.Error("expected Read to create nothing")
	}
}
