package infra_test

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/app"
	"github.com/apsdsm/joka/internal/domains/entity/domain"
	"github.com/apsdsm/joka/internal/domains/entity/infra"
	"github.com/apsdsm/joka/testlib"
)

// The backend is what the app layer is handed, so it has to satisfy the
// interface the app layer declares.
var _ app.StateBackend = (*infra.PostgresStateBackend)(nil)

// bareDB returns a database with no joka_* tables at all.
func bareDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	for _, table := range []string{"joka_state", "joka_meta", "joka_entity_rows", "joka_entities"} {
		testlib.DropTablePostgres(t, db, table)
	}

	t.Cleanup(func() {
		for _, table := range []string{"joka_state", "joka_meta", "joka_entity_rows", "joka_entities"} {
			testlib.DropTablePostgres(t, db, table)
		}
	})

	return db
}

// freshStateDB returns a database with joka_state present and empty.
func freshStateDB(t *testing.T) *sql.DB {
	t.Helper()

	db := bareDB(t)

	if err := infra.NewPostgresStateBackend(db).EnsureStateTable(context.Background()); err != nil {
		t.Fatalf("EnsureStateTable: %v", err)
	}

	return db
}

// legacyStateDB returns a database in the shape tracking versions 1 and 2 used:
// the two decomposed tables, no joka_state. This is what a database that no
// mutating command has touched since the version 3 upgrade shipped looks like.
func legacyStateDB(t *testing.T) *sql.DB {
	t.Helper()

	db := bareDB(t)

	ctx := context.Background()
	adapter := infra.NewPostgresDBAdapter(db)

	if err := adapter.EnsureTrackingTable(ctx); err != nil {
		t.Fatalf("EnsureTrackingTable: %v", err)
	}
	if err := adapter.EnsureContentHashColumn(ctx); err != nil {
		t.Fatalf("EnsureContentHashColumn: %v", err)
	}
	if err := adapter.EnsureRowTrackingTable(ctx); err != nil {
		t.Fatalf("EnsureRowTrackingTable: %v", err)
	}

	return db
}

func TestPostgresStateBackendLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()

	t.Run("a database with nothing to read loads as empty, not an error", func(t *testing.T) {
		db := bareDB(t)

		state, err := infra.NewPostgresStateBackend(db).Load(ctx)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(state.Files) != 0 || len(state.Entities) != 0 {
			t.Errorf("expected an empty state, got %d files and %d entities", len(state.Files), len(state.Entities))
		}

		// A read must not create what it reads, which is what makes a missing
		// tracking table reportable.
		var exists bool
		if err := db.QueryRowContext(ctx,
			`SELECT to_regclass('joka_state') IS NOT NULL`).Scan(&exists); err != nil {
			t.Fatalf("checking for joka_state: %v", err)
		}
		if exists {
			t.Error("expected the read to create nothing")
		}
	})

	t.Run("a state table with no document yet loads as empty", func(t *testing.T) {
		db := freshStateDB(t)

		state, err := infra.NewPostgresStateBackend(db).Load(ctx)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(state.Files) != 0 || len(state.Entities) != 0 {
			t.Errorf("expected an empty state, got %d files and %d entities", len(state.Files), len(state.Entities))
		}
	})

	t.Run("it reads the decomposed layout when there is no document", func(t *testing.T) {
		db := legacyStateDB(t)

		if _, err := db.ExecContext(ctx,
			`INSERT INTO joka_entities (entity_file, content_hash) VALUES ('a.yaml', 'hash-a')`); err != nil {
			t.Fatalf("seeding joka_entities: %v", err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO joka_entity_rows (entity_file, table_name, row_pk, pk_column, ref_id, insertion_order)
			 VALUES ('a.yaml', 'users', 7, 'id', 'admin', 0)`); err != nil {
			t.Fatalf("seeding joka_entity_rows: %v", err)
		}

		state, err := infra.NewPostgresStateBackend(db).Load(ctx)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}

		if hash, tracked := state.FileHash("a.yaml"); !tracked || hash != "hash-a" {
			t.Errorf("expected a.yaml at hash-a, got tracked=%v hash=%q", tracked, hash)
		}
		admin, ok := state.Row("admin")
		if !ok || admin.PKValue != 7 || admin.Table != "users" {
			t.Errorf("expected the tracked row read from the old tables, got %+v ok=%v", admin, ok)
		}
	})

	t.Run("the document wins when both layouts are present", func(t *testing.T) {
		// A database mid-upgrade, or one whose old tables a DROP did not reach.
		// The document is what the current joka wrote, so it is the answer.
		db := legacyStateDB(t)

		if _, err := db.ExecContext(ctx,
			`INSERT INTO joka_entities (entity_file, content_hash) VALUES ('stale.yaml', 'stale')`); err != nil {
			t.Fatalf("seeding joka_entities: %v", err)
		}

		backend := infra.NewPostgresStateBackend(db)
		if err := backend.EnsureStateTable(ctx); err != nil {
			t.Fatalf("EnsureStateTable: %v", err)
		}

		want := domain.NewState()
		want.TrackFile("current.yaml", "current")
		if err := backend.Save(ctx, want); err != nil {
			t.Fatalf("Save: %v", err)
		}

		got, err := backend.Load(ctx)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if _, tracked := got.FileHash("stale.yaml"); tracked {
			t.Error("expected the old tables ignored once a document exists")
		}
		if _, tracked := got.FileHash("current.yaml"); !tracked {
			t.Error("expected the document read")
		}
	})

	t.Run("it carries a legacy row with no _id rather than dropping it", func(t *testing.T) {
		db := legacyStateDB(t)

		if _, err := db.ExecContext(ctx,
			`INSERT INTO joka_entity_rows (entity_file, table_name, row_pk, pk_column, ref_id, insertion_order)
			 VALUES ('legacy.yaml', 'users', 5, 'id', '', 0)`); err != nil {
			t.Fatalf("seeding a legacy row: %v", err)
		}

		state, err := infra.NewPostgresStateBackend(db).Load(ctx)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}

		if len(state.Entities) != 0 {
			t.Errorf("expected nothing keyed, got %d entities", len(state.Entities))
		}
		if len(state.Unkeyed) != 1 || state.Unkeyed[0].RowPK != 5 {
			t.Fatalf("expected the legacy row carried as unkeyed, got %+v", state.Unkeyed)
		}
	})

	t.Run("it refuses a legacy database where one _id is claimed twice", func(t *testing.T) {
		db := legacyStateDB(t)

		// Reachable only on a database whose tracking version 2 upgrade is
		// blocked on exactly this, so the table has to be put back in its
		// pre-upgrade shape to reach it. The document cannot represent two rows
		// under one _id, so saying so beats picking one of them.
		if _, err := db.ExecContext(ctx, `DROP INDEX `+infra.RefIDIndex); err != nil {
			t.Fatalf("removing the unique index: %v", err)
		}

		if _, err := db.ExecContext(ctx,
			`INSERT INTO joka_entity_rows (entity_file, table_name, row_pk, pk_column, ref_id, insertion_order)
			 VALUES ('local.yaml', 'users', 1, 'id', 'admin', 0),
			        ('dev1.yaml',  'users', 2, 'id', 'admin', 0)`); err != nil {
			t.Fatalf("seeding duplicate claims: %v", err)
		}

		_, err := infra.NewPostgresStateBackend(db).Load(ctx)
		if !errors.Is(err, domain.ErrStateAmbiguous) {
			t.Fatalf("expected ErrStateAmbiguous, got %v", err)
		}
	})
}

func TestPostgresStateBackendSave(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()

	t.Run("it round-trips a state through the database", func(t *testing.T) {
		db := freshStateDB(t)
		backend := infra.NewPostgresStateBackend(db)

		want := domain.NewState()
		want.TrackFile("users.yaml", "hash-a")
		want.TrackFile("legacy.yaml", "")
		want.Track("admin", domain.EntityState{
			Table: "users", PKColumn: "id", PKValue: 7, File: "users.yaml", Order: 0,
		})
		want.Track("admin_profile", domain.EntityState{
			Table: "profiles", PKColumn: "id", PKValue: 9, File: "users.yaml", Order: 1,
		})

		if err := backend.Save(ctx, want); err != nil {
			t.Fatalf("Save: %v", err)
		}

		got, err := backend.Load(ctx)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}

		if got.Version != domain.StateVersion {
			t.Errorf("expected version %d, got %d", domain.StateVersion, got.Version)
		}
		if len(got.Files) != 2 {
			t.Errorf("expected 2 files, got %d", len(got.Files))
		}
		if hash, tracked := got.FileHash("users.yaml"); !tracked || hash != "hash-a" {
			t.Errorf("expected users.yaml at hash-a, got tracked=%v hash=%q", tracked, hash)
		}
		if hash, tracked := got.FileHash("legacy.yaml"); !tracked || hash != "" {
			t.Errorf("expected legacy.yaml tracked with no hash, got tracked=%v hash=%q", tracked, hash)
		}

		admin, ok := got.Row("admin")
		if !ok {
			t.Fatal("expected admin tracked")
		}
		if !reflect.DeepEqual(admin, want.Entities["admin"]) {
			t.Errorf("expected %+v, got %+v", want.Entities["admin"], admin)
		}
	})

	t.Run("saving twice leaves one document", func(t *testing.T) {
		db := freshStateDB(t)
		backend := infra.NewPostgresStateBackend(db)

		state := domain.NewState()
		state.TrackFile("users.yaml", "hash-a")
		state.Track("admin", domain.EntityState{
			Table: "users", PKColumn: "id", PKValue: 7, File: "users.yaml",
		})

		if err := backend.Save(ctx, state); err != nil {
			t.Fatalf("first Save: %v", err)
		}
		if err := backend.Save(ctx, state); err != nil {
			t.Fatalf("second Save: %v", err)
		}

		var docs int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM joka_state`).Scan(&docs); err != nil {
			t.Fatalf("counting documents: %v", err)
		}
		if docs != 1 {
			t.Errorf("expected 1 document after two saves, got %d", docs)
		}
	})

	t.Run("it re-points a row that moved file without changing the row it names", func(t *testing.T) {
		db := freshStateDB(t)
		backend := infra.NewPostgresStateBackend(db)

		state := domain.NewState()
		state.TrackFile("a.yaml", "hash-a")
		state.Track("admin", domain.EntityState{
			Table: "users", PKColumn: "id", PKValue: 7, File: "a.yaml", Order: 0,
		})
		if err := backend.Save(ctx, state); err != nil {
			t.Fatalf("first Save: %v", err)
		}

		state.ForgetFile("a.yaml")
		state.TrackFile("b.yaml", "hash-b")
		state.Track("admin", domain.EntityState{
			Table: "users", PKColumn: "id", PKValue: 7, File: "b.yaml", Order: 3,
		})
		if err := backend.Save(ctx, state); err != nil {
			t.Fatalf("second Save: %v", err)
		}

		got, err := backend.Load(ctx)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}

		admin, ok := got.Row("admin")
		if !ok {
			t.Fatal("expected admin still tracked")
		}
		if admin.File != "b.yaml" || admin.Order != 3 {
			t.Errorf("expected the tracking re-pointed at b.yaml position 3, got %+v", admin)
		}
		if admin.PKValue != 7 {
			t.Errorf("expected the row it names unchanged, got pk %d", admin.PKValue)
		}
		if _, tracked := got.FileHash("a.yaml"); tracked {
			t.Error("expected a.yaml's record dropped")
		}
	})

	t.Run("dropping an entry removes its tracking and nothing else", func(t *testing.T) {
		db := freshStateDB(t)
		backend := infra.NewPostgresStateBackend(db)

		state := domain.NewState()
		state.Track("admin", domain.EntityState{Table: "users", PKColumn: "id", PKValue: 7, File: "a.yaml"})
		state.Track("guest", domain.EntityState{Table: "users", PKColumn: "id", PKValue: 8, File: "a.yaml", Order: 1})
		if err := backend.Save(ctx, state); err != nil {
			t.Fatalf("first Save: %v", err)
		}

		state.Forget("admin")
		if err := backend.Save(ctx, state); err != nil {
			t.Fatalf("second Save: %v", err)
		}

		got, err := backend.Load(ctx)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if _, ok := got.Row("admin"); ok {
			t.Error("expected admin's tracking removed")
		}
		if _, ok := got.Row("guest"); !ok {
			t.Error("expected guest's tracking to survive")
		}
	})

	t.Run("a rolled-back transaction leaves no tracking behind", func(t *testing.T) {
		db := freshStateDB(t)

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}

		state := domain.NewState()
		state.TrackFile("a.yaml", "hash-a")
		state.Track("admin", domain.EntityState{Table: "users", PKColumn: "id", PKValue: 7, File: "a.yaml"})

		if err := infra.NewPostgresTxStateBackend(tx, db).Save(ctx, state); err != nil {
			t.Fatalf("Save: %v", err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatalf("Rollback: %v", err)
		}

		got, err := infra.NewPostgresStateBackend(db).Load(ctx)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(got.Files) != 0 || len(got.Entities) != 0 {
			t.Errorf("expected nothing committed, got %d files and %d entities", len(got.Files), len(got.Entities))
		}
	})
}
