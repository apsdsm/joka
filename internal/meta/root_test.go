package meta_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/meta"
)

func TestCheckRoot(t *testing.T) {
	t.Run("an unclaimed database is open to anyone", func(t *testing.T) {
		// Every database until some configuration declares a root. joka worked
		// this way before roots existed and has to carry on working that way.
		if err := meta.CheckRoot("", "", false); err != nil {
			t.Errorf("expected no error with nothing declared, got: %v", err)
		}
		if err := meta.CheckRoot("", "jjc2-local", false); err != nil {
			t.Errorf("expected no error claiming an unclaimed database, got: %v", err)
		}
	})

	t.Run("the owning root proceeds", func(t *testing.T) {
		if err := meta.CheckRoot("jjc2-local", "jjc2-local", false); err != nil {
			t.Errorf("expected the owning root to proceed, got: %v", err)
		}
	})

	t.Run("another root is refused, and both are named", func(t *testing.T) {
		err := meta.CheckRoot("jjc2-local", "jjc2-e2e", false)
		if !errors.Is(err, meta.ErrWrongRoot) {
			t.Fatalf("expected ErrWrongRoot, got: %v", err)
		}
		for _, want := range []string{"jjc2-local", "jjc2-e2e", "--adopt-root"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("expected the refusal to mention %q, got: %v", want, err)
			}
		}
	})

	t.Run("a claimed database refuses a configuration that declares no root", func(t *testing.T) {
		// This row is what makes the guard hold. If an undeclared root went
		// through, deleting one line from a configuration would turn the
		// protection off — and what it protects against is a second root
		// deleting the first root's rows as declared nowhere, silently.
		err := meta.CheckRoot("jjc2-local", "", false)
		if !errors.Is(err, meta.ErrWrongRoot) {
			t.Fatalf("expected ErrWrongRoot, got: %v", err)
		}
		if !strings.Contains(err.Error(), "declares no root") {
			t.Errorf("expected the refusal to say no root was declared, got: %v", err)
		}
	})

	t.Run("--adopt-root moves the claim", func(t *testing.T) {
		if err := meta.CheckRoot("jjc2-local", "jjc2-e2e", true); err != nil {
			t.Errorf("expected --adopt-root to allow the move, got: %v", err)
		}
	})
}

func TestClaimRoot(t *testing.T) {
	ctx := context.Background()
	db := freshDB(t)

	t.Run("an empty root claims nothing", func(t *testing.T) {
		// A project that declares no root must not have joka_meta created for
		// it, or a read-only command run afterwards would report bookkeeping
		// that nothing asked for.
		if err := meta.ClaimRoot(ctx, db, ""); err != nil {
			t.Fatalf("ClaimRoot: %v", err)
		}
		exists, err := jokadb.TableExists(ctx, db, meta.Table)
		if err != nil {
			t.Fatalf("TableExists: %v", err)
		}
		if exists {
			t.Error("expected an empty root to create nothing")
		}
	})

	t.Run("the first root claims it", func(t *testing.T) {
		if err := meta.ClaimRoot(ctx, db, "jjc2-local"); err != nil {
			t.Fatalf("ClaimRoot: %v", err)
		}
		if got := readRoot(t, db); got != "jjc2-local" {
			t.Errorf("expected jjc2-local, got %q", got)
		}
	})

	t.Run("a second root does not take it", func(t *testing.T) {
		// DO NOTHING, not DO UPDATE. A second root overwriting the first is
		// exactly what CheckRoot exists to stop, so the write must not be able
		// to do it even if the check were somehow bypassed.
		if err := meta.ClaimRoot(ctx, db, "jjc2-e2e"); err != nil {
			t.Fatalf("ClaimRoot: %v", err)
		}
		if got := readRoot(t, db); got != "jjc2-local" {
			t.Errorf("expected the first claim to stand, got %q", got)
		}
	})

	t.Run("adopting moves it", func(t *testing.T) {
		if err := meta.AdoptRoot(ctx, db, "jjc2-e2e"); err != nil {
			t.Fatalf("AdoptRoot: %v", err)
		}
		if got := readRoot(t, db); got != "jjc2-e2e" {
			t.Errorf("expected jjc2-e2e after adopting, got %q", got)
		}
	})

	t.Run("Read reports it", func(t *testing.T) {
		state, err := meta.Read(ctx, db)
		if err != nil {
			t.Fatalf("Read: %v", err)
		}
		if state.StateRoot != "jjc2-e2e" {
			t.Errorf("expected Read to report jjc2-e2e, got %q", state.StateRoot)
		}
	})
}

func readRoot(t *testing.T, db *sql.DB) string {
	t.Helper()

	var value string
	err := db.QueryRow(`SELECT value FROM `+meta.Table+` WHERE key = $1`, meta.KeyStateRoot).Scan(&value)
	if err == sql.ErrNoRows {
		return ""
	}
	if err != nil {
		t.Fatalf("reading the root: %v", err)
	}
	return value
}
