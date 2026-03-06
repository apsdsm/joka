package infra_test

import (
	"context"
	"errors"
	"testing"

	"github.com/apsdsm/joka/internal/domains/lock/domain"
	"github.com/apsdsm/joka/internal/domains/lock/infra"
	"github.com/apsdsm/joka/testlib"
)

func TestPostgresEnsureTable(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Cleanup(func() { testlib.DropTablePostgres(t, db, "joka_lock") })

	t.Run("it creates the table and is idempotent", func(t *testing.T) {
		adapter := infra.NewPostgresLockAdapter(db)
		ctx := context.Background()

		if err := adapter.EnsureTable(ctx); err != nil {
			t.Fatalf("first EnsureTable: %v", err)
		}

		if err := adapter.EnsureTable(ctx); err != nil {
			t.Fatalf("second EnsureTable: %v", err)
		}
	})
}

func TestPostgresAcquire(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Cleanup(func() { testlib.DropTablePostgres(t, db, "joka_lock") })

	adapter := infra.NewPostgresLockAdapter(db)
	ctx := context.Background()

	t.Run("it acquires the lock successfully", func(t *testing.T) {
		if err := adapter.Acquire(ctx, "migrate up"); err != nil {
			t.Fatalf("Acquire: %v", err)
		}

		if err := adapter.Release(ctx); err != nil {
			t.Fatalf("Release: %v", err)
		}
	})

	t.Run("it returns ErrLockHeld when lock is already held", func(t *testing.T) {
		if err := adapter.Acquire(ctx, "migrate up"); err != nil {
			t.Fatalf("first Acquire: %v", err)
		}

		err := adapter.Acquire(ctx, "data sync")
		if err == nil {
			t.Fatal("expected error on second Acquire, got nil")
		}
		if !errors.Is(err, domain.ErrLockHeld) {
			t.Fatalf("expected ErrLockHeld, got: %v", err)
		}

		if err := adapter.Release(ctx); err != nil {
			t.Fatalf("Release: %v", err)
		}
	})
}

func TestPostgresReleaseThenAcquire(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Cleanup(func() { testlib.DropTablePostgres(t, db, "joka_lock") })

	t.Run("it allows re-acquiring after release", func(t *testing.T) {
		adapter := infra.NewPostgresLockAdapter(db)
		ctx := context.Background()

		if err := adapter.Acquire(ctx, "migrate up"); err != nil {
			t.Fatalf("first Acquire: %v", err)
		}
		if err := adapter.Release(ctx); err != nil {
			t.Fatalf("Release: %v", err)
		}

		if err := adapter.Acquire(ctx, "data sync"); err != nil {
			t.Fatalf("second Acquire after Release: %v", err)
		}
		if err := adapter.Release(ctx); err != nil {
			t.Fatalf("final Release: %v", err)
		}
	})
}

func TestPostgresGetLock(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	t.Cleanup(func() { testlib.DropTablePostgres(t, db, "joka_lock") })

	adapter := infra.NewPostgresLockAdapter(db)
	ctx := context.Background()

	t.Run("it returns nil when no lock is held", func(t *testing.T) {
		if err := adapter.EnsureTable(ctx); err != nil {
			t.Fatalf("EnsureTable: %v", err)
		}

		lock, err := adapter.GetLock(ctx)
		if err != nil {
			t.Fatalf("GetLock: %v", err)
		}
		if lock != nil {
			t.Fatalf("expected nil lock when none held, got: %+v", lock)
		}
	})

	t.Run("it returns lock details when a lock is held", func(t *testing.T) {
		if err := adapter.Acquire(ctx, "migrate up"); err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		t.Cleanup(func() { adapter.Release(ctx) }) //nolint:errcheck

		lock, err := adapter.GetLock(ctx)
		if err != nil {
			t.Fatalf("GetLock: %v", err)
		}
		if lock == nil {
			t.Fatal("expected lock details, got nil")
		}
		if lock.Operation != "migrate up" {
			t.Errorf("expected operation 'migrate up', got %q", lock.Operation)
		}
		if lock.LockedBy == "" {
			t.Error("expected non-empty LockedBy")
		}
	})
}
