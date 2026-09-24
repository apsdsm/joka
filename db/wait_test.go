package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	jokadb "github.com/apsdsm/joka/db"
)

func TestOpenWait(t *testing.T) {
	// A DSN pointing at a port nothing listens on: the failure mode a
	// container entrypoint hits before the database has started.
	const unreachable = "postgresql://nobody:nobody@127.0.0.1:1/none?sslmode=disable&connect_timeout=1"

	t.Run("a DSN joka cannot use is refused immediately, not waited out", func(t *testing.T) {
		start := time.Now()
		_, err := jokadb.OpenWait(context.Background(), "mysql://root@localhost/app", 30*time.Second)
		if !errors.Is(err, jokadb.ErrUnsupportedDriver) {
			t.Fatalf("expected ErrUnsupportedDriver, got: %v", err)
		}
		// Waiting out a timeout for a URL that can never work is the opposite
		// of helpful.
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("expected an immediate refusal, took %s", elapsed)
		}
	})

	t.Run("it gives up at the deadline and says how long it waited", func(t *testing.T) {
		start := time.Now()
		_, err := jokadb.OpenWait(context.Background(), unreachable, 1200*time.Millisecond)
		if err == nil {
			t.Fatal("expected an error for an unreachable database")
		}
		if !strings.Contains(err.Error(), "not reachable after") {
			t.Errorf("expected the error to say it waited, got: %v", err)
		}
		if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
			t.Errorf("expected it to wait out the deadline, gave up after %s", elapsed)
		}
	})

	t.Run("a cancelled context stops the wait", func(t *testing.T) {
		// The caller giving up is not the same as the database being slow.
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()

		start := time.Now()
		if _, err := jokadb.OpenWait(ctx, unreachable, 30*time.Second); err == nil {
			t.Fatal("expected an error")
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("expected the cancellation to be honoured, took %s", elapsed)
		}
	})

	t.Run("zero waits not at all", func(t *testing.T) {
		start := time.Now()
		if _, err := jokadb.OpenWait(context.Background(), unreachable, 0); err == nil {
			t.Fatal("expected an error")
		}
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Errorf("expected a single attempt, took %s", elapsed)
		}
	})
}
