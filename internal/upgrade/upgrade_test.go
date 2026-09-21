package upgrade_test

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"strings"
	"testing"

	jokadb "github.com/apsdsm/joka/db"
	entityinfra "github.com/apsdsm/joka/internal/domains/entity/infra"
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
		testlib.DropTablePostgres(t, db, "joka_state")
		testlib.DropTablePostgres(t, db, "joka_entity_rows")
		testlib.DropTablePostgres(t, db, "joka_entities")
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

// trackedIDs reads the _ids the state document holds, sorted.
func trackedIDs(t *testing.T, db *sql.DB) []string {
	t.Helper()

	state, err := entityinfra.NewPostgresStateBackend(db).Load(context.Background())
	if err != nil {
		t.Fatalf("loading the state: %v", err)
	}

	ids := make([]string, 0, len(state.Entities))
	for id := range state.Entities {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// tableExists reports whether the named table is still in the database.
func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()

	var exists bool
	if err := db.QueryRow(`SELECT to_regclass($1) IS NOT NULL`, name).Scan(&exists); err != nil {
		t.Fatalf("checking for %s: %v", name, err)
	}
	return exists
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

	if len(applied) != 2 || applied[0].To != 2 || applied[1].To != 3 {
		t.Fatalf("expected steps to version 2 then 3, got %+v", applied)
	}

	// Both rows are in the document, and the tables they came from are gone —
	// leaving them would be a second copy of what the document now holds.
	if got := trackedIDs(t, db); len(got) != 2 || got[0] != "alpha" || got[1] != "beta" {
		t.Errorf("expected alpha and beta in the document, got %v", got)
	}
	for _, table := range []string{"joka_entity_rows", "joka_entities"} {
		if tableExists(t, db, table) {
			t.Errorf("expected %s dropped", table)
		}
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
	if tableExists(t, db, "joka_state") {
		t.Error("expected no state document after a blocked upgrade")
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
	if tableExists(t, db, "joka_state") {
		t.Error("expected no state document after a blocked upgrade")
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
	if len(applied) != 2 {
		t.Errorf("expected both steps to run, got %+v", applied)
	}
	if got := trackedIDs(t, db); len(got) != 1 || got[0] != "role_owner" {
		t.Errorf("expected the surviving claim in the document, got %v", got)
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

	// An upgrade may move a record; it never changes what a row means. Both
	// entities still point at the primary keys they pointed at before.
	state, err := entityinfra.NewPostgresStateBackend(db).Load(ctx)
	if err != nil {
		t.Fatalf("loading the state: %v", err)
	}
	if len(state.Entities) != 2 {
		t.Fatalf("expected both entities carried over, got %d", len(state.Entities))
	}
	if alpha, ok := state.Row("alpha"); !ok || alpha.PKValue != 1 || alpha.Table != "fields" {
		t.Errorf("expected alpha to still name fields row 1, got %+v ok=%v", alpha, ok)
	}
	if beta, ok := state.Row("beta"); !ok || beta.PKValue != 2 {
		t.Errorf("expected beta to still name row 2, got %+v ok=%v", beta, ok)
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
