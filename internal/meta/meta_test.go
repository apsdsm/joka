package meta_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/meta"
	"github.com/apsdsm/joka/testlib"
)

func freshDB(t *testing.T) *sql.DB {
	t.Helper()

	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	drop := func() { testlib.DropTablePostgres(t, db, meta.Table) }
	drop()
	t.Cleanup(drop)

	return db
}

func TestReadOnBareDatabase(t *testing.T) {
	db := freshDB(t)

	state, err := meta.Read(context.Background(), db)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if state.Present {
		t.Error("expected no marker on a bare database")
	}
	// A database with no marker predates the marker; it is not from the future.
	if state.TrackingVersion != meta.TrackingVersion {
		t.Errorf("expected an absent marker to read as the current version, got %d", state.TrackingVersion)
	}
}

func TestReadCreatesNothing(t *testing.T) {
	db := freshDB(t)
	ctx := context.Background()

	if _, err := meta.Read(ctx, db); err != nil {
		t.Fatalf("Read: %v", err)
	}

	exists, err := jokadb.TableExists(ctx, db, meta.Table)
	if err != nil {
		t.Fatalf("TableExists: %v", err)
	}
	if exists {
		t.Error("expected Read to create nothing — status depends on it")
	}
}

func TestStampAndRead(t *testing.T) {
	db := freshDB(t)
	ctx := context.Background()

	if err := meta.Stamp(ctx, db, "1.2.3"); err != nil {
		t.Fatalf("Stamp: %v", err)
	}

	state, err := meta.Read(ctx, db)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if !state.Present {
		t.Error("expected the marker present after stamping")
	}
	if state.TrackingVersion != meta.TrackingVersion {
		t.Errorf("expected version %d, got %d", meta.TrackingVersion, state.TrackingVersion)
	}
	if state.JokaVersion != "1.2.3" {
		t.Errorf("expected the joka version recorded, got %q", state.JokaVersion)
	}
	if state.UpdatedAt == "" {
		t.Error("expected an updated_at timestamp")
	}
}

func TestStampIsIdempotent(t *testing.T) {
	db := freshDB(t)
	ctx := context.Background()

	if err := meta.Stamp(ctx, db, "1.0.0"); err != nil {
		t.Fatalf("first Stamp: %v", err)
	}
	if err := meta.Stamp(ctx, db, "2.0.0"); err != nil {
		t.Fatalf("second Stamp: %v", err)
	}

	// Every mutating command stamps, so this runs constantly; it must update
	// in place rather than accumulate rows.
	var rows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM `+meta.Table).Scan(&rows); err != nil {
		t.Fatalf("counting rows: %v", err)
	}
	if rows != 2 {
		t.Errorf("expected exactly one row per key, got %d", rows)
	}

	state, err := meta.Read(ctx, db)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if state.JokaVersion != "2.0.0" {
		t.Errorf("expected the latest writer recorded, got %q", state.JokaVersion)
	}
}

func TestCheck(t *testing.T) {
	db := freshDB(t)
	ctx := context.Background()

	t.Run("it accepts a bare database", func(t *testing.T) {
		if err := meta.Check(ctx, db); err != nil {
			t.Errorf("expected no error on a bare database, got %v", err)
		}
	})

	t.Run("it accepts the current version", func(t *testing.T) {
		if err := meta.Stamp(ctx, db, "0.14.0"); err != nil {
			t.Fatalf("Stamp: %v", err)
		}
		if err := meta.Check(ctx, db); err != nil {
			t.Errorf("expected no error at the current version, got %v", err)
		}
	})

	t.Run("it refuses a database written by a newer joka", func(t *testing.T) {
		if _, err := db.ExecContext(ctx,
			`UPDATE `+meta.Table+` SET value = '99' WHERE key = $1`, meta.KeyTrackingVersion,
		); err != nil {
			t.Fatalf("bumping the version: %v", err)
		}

		err := meta.Check(ctx, db)
		if !errors.Is(err, meta.ErrTrackingVersionTooNew) {
			t.Fatalf("expected ErrTrackingVersionTooNew, got %v", err)
		}
		// The error has to name both versions and the joka that wrote it —
		// that is the whole point of recording them.
		for _, want := range []string{"99", "0.14.0", "upgrade joka"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("expected %q in %q", want, err)
			}
		}
	})

	t.Run("it refuses a version it cannot parse", func(t *testing.T) {
		if _, err := db.ExecContext(ctx,
			`UPDATE `+meta.Table+` SET value = 'banana' WHERE key = $1`, meta.KeyTrackingVersion,
		); err != nil {
			t.Fatalf("setting the version: %v", err)
		}

		// joka writes a decimal string, so anything else came from something
		// this build does not understand.
		if _, err := meta.Read(ctx, db); !errors.Is(err, meta.ErrTrackingVersionTooNew) {
			t.Fatalf("expected ErrTrackingVersionTooNew, got %v", err)
		}
	})
}
