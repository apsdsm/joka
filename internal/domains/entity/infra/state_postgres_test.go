package infra_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/app"
	"github.com/apsdsm/joka/internal/domains/entity/domain"
	"github.com/apsdsm/joka/internal/domains/entity/infra"
	"github.com/apsdsm/joka/testlib"
)

// The backend is what the app layer is handed, so it has to satisfy the
// interface the app layer declares.
var _ app.StateBackend = (*infra.PostgresStateBackend)(nil)

// freshStateDB returns a database with the entity tracking tables present and
// empty, and registers their cleanup.
func freshStateDB(t *testing.T) *sql.DB {
	t.Helper()

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	ctx := context.Background()
	if err := infra.NewPostgresDBAdapter(db).EnsureTables(ctx); err != nil {
		t.Fatalf("EnsureTables: %v", err)
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM joka_entity_rows`); err != nil {
		t.Fatalf("clearing joka_entity_rows: %v", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM joka_entities`); err != nil {
		t.Fatalf("clearing joka_entities: %v", err)
	}

	t.Cleanup(func() {
		testlib.DropTablePostgres(t, db, "joka_entity_rows")
		testlib.DropTablePostgres(t, db, "joka_entities")
	})

	return db
}

func TestPostgresStateBackendLoad(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	ctx := context.Background()

	t.Run("a database with no tracking tables loads as empty, not an error", func(t *testing.T) {
		db, err := testlib.GetTestPostgresDB()
		if err != nil {
			t.Fatalf("getting test db: %v", err)
		}
		// Deliberately no EnsureTables: a read must not create what it reads,
		// which is what makes a missing tracking table reportable.
		testlib.DropTablePostgres(t, db, "joka_entity_rows")
		testlib.DropTablePostgres(t, db, "joka_entities")

		state, err := infra.NewPostgresStateBackend(db).Load(ctx)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		if len(state.Files) != 0 || len(state.Entities) != 0 {
			t.Errorf("expected an empty state, got %d files and %d entities", len(state.Files), len(state.Entities))
		}
	})

	t.Run("it carries a row with no _id rather than dropping it", func(t *testing.T) {
		db := freshStateDB(t)

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

	t.Run("it refuses a database where one _id is claimed twice", func(t *testing.T) {
		db := freshStateDB(t)

		// Reachable only on a database whose tracking version 2 upgrade is
		// blocked on exactly this, so the table has to be put back in its
		// pre-upgrade shape to reach it. The document cannot represent two rows
		// under one _id, so saying so beats picking one of them.
		if _, err := db.ExecContext(ctx,
			`DROP INDEX `+infra.RefIDIndex); err != nil {
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
		if admin != want.Entities["admin"] {
			t.Errorf("expected %+v, got %+v", want.Entities["admin"], admin)
		}
	})

	t.Run("saving twice changes nothing", func(t *testing.T) {
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

		var rows int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM joka_entity_rows`).Scan(&rows); err != nil {
			t.Fatalf("counting tracked rows: %v", err)
		}
		if rows != 1 {
			t.Errorf("expected 1 tracked row after two saves, got %d", rows)
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

	t.Run("it leaves a row with no _id alone", func(t *testing.T) {
		db := freshStateDB(t)
		backend := infra.NewPostgresStateBackend(db)

		if _, err := db.ExecContext(ctx,
			`INSERT INTO joka_entity_rows (entity_file, table_name, row_pk, pk_column, ref_id, insertion_order)
			 VALUES ('legacy.yaml', 'users', 5, 'id', '', 0)`); err != nil {
			t.Fatalf("seeding a legacy row: %v", err)
		}

		state, err := backend.Load(ctx)
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		state.Track("admin", domain.EntityState{Table: "users", PKColumn: "id", PKValue: 7, File: "a.yaml"})

		if err := backend.Save(ctx, state); err != nil {
			t.Fatalf("Save: %v", err)
		}

		// The unkeyed row is not something Save can address — it has no _id to
		// match on — so it must survive rather than be swept up as "not in the
		// document".
		after, err := backend.Load(ctx)
		if err != nil {
			t.Fatalf("Load after save: %v", err)
		}
		if len(after.Unkeyed) != 1 || after.Unkeyed[0].RowPK != 5 {
			t.Errorf("expected the legacy row untouched, got %+v", after.Unkeyed)
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
