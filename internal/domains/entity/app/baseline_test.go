package app

import (
	"context"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

func TestHashValue(t *testing.T) {
	t.Run("the same value hashes the same way twice", func(t *testing.T) {
		if HashValue("alpha") != HashValue("alpha") {
			t.Error("expected a stable hash")
		}
	})

	t.Run("different values hash differently", func(t *testing.T) {
		if HashValue("alpha") == HashValue("beta") {
			t.Error("expected different hashes")
		}
	})

	t.Run("it renders a value the way the plan compares it", func(t *testing.T) {
		// The baseline and the diff have to agree about what a value is, or a
		// column would read as changed on one and unchanged on the other.
		if HashValue([]byte("alpha")) != HashValue("alpha") {
			t.Error("expected a byte slice and its string to hash alike")
		}
		if HashValue(nil) != HashValue("NULL") {
			t.Error("expected nil rendered as NULL, the way normalizeValue renders it")
		}
	})
}

func TestBaselineOf(t *testing.T) {
	t.Run("it hashes every column", func(t *testing.T) {
		got := BaselineOf(map[string]any{"name": "Admin", "email": "a@b.test"})

		if len(got) != 2 {
			t.Fatalf("expected 2 columns, got %d", len(got))
		}
		if got["name"] != HashValue("Admin") {
			t.Errorf("expected name hashed, got %q", got["name"])
		}
	})

	t.Run("a row with no columns has no baseline", func(t *testing.T) {
		if got := BaselineOf(nil); got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})
}

func TestApplySetRecordsTheBaseline(t *testing.T) {
	db := newMockDBAdapter()
	file := entityFile("a.yaml", col("fields", "alpha", map[string]any{"label": "Alpha"}))

	applyAll(t, db, file)

	alpha, ok := db.state.Row("alpha")
	if !ok {
		t.Fatal("expected alpha tracked")
	}

	hash, recorded := alpha.Baseline("label")
	if !recorded {
		t.Fatal("expected a baseline for label")
	}
	if hash != HashValue("Alpha") {
		t.Errorf("expected the applied value hashed, got %q", hash)
	}
}

func TestApplySetRewritesTheBaselineOnUpdate(t *testing.T) {
	db := newMockDBAdapter()

	before := entityFile("a.yaml", col("fields", "alpha", map[string]any{"label": "Alpha"}))
	applyAll(t, db, before)

	// The row was just rewritten, so what joka last applied is the new value —
	// the baseline has to move with it or the next run reads the change twice.
	after := entityFile("a.yaml", col("fields", "alpha", map[string]any{"label": "Renamed"}))
	after.ContentHash = "hash-changed"
	applyAll(t, db, after)

	alpha, ok := db.state.Row("alpha")
	if !ok {
		t.Fatal("expected alpha still tracked")
	}
	if hash, _ := alpha.Baseline("label"); hash != HashValue("Renamed") {
		t.Errorf("expected the baseline moved to the new value, got %q", hash)
	}
}

func TestApplySetLeavesACleanFilesBaselineAlone(t *testing.T) {
	db := newMockDBAdapter()

	a := entityFile("a.yaml", col("fields", "alpha", map[string]any{"label": "Alpha"}))
	b := entityFile("b.yaml", col("fields", "beta", map[string]any{"label": "Beta"}))
	applyAll(t, db, a, b)

	// Only b.yaml is dirty. a.yaml is not written, so its baseline must survive
	// the save rather than being dropped for want of a rewrite.
	if _, err := (ApplySetAction{
		DB:       db,
		Backend:  db.backend(),
		Declared: []*domain.EntityFile{a, b},
		Dirty:    map[string]bool{"b.yaml": true},
	}).Execute(context.Background()); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	alpha, ok := db.state.Row("alpha")
	if !ok {
		t.Fatal("expected alpha still tracked")
	}
	if hash, recorded := alpha.Baseline("label"); !recorded || hash != HashValue("Alpha") {
		t.Errorf("expected the untouched file's baseline intact, got %q recorded=%v", hash, recorded)
	}
}
