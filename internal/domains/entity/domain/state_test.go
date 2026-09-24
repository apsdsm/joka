package domain_test

import (
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

func row(file, table string, pk int64, order int) domain.EntityState {
	return domain.EntityState{Table: table, PKColumn: "id", PKValue: pk, File: file, Order: order}
}

func TestState(t *testing.T) {
	t.Run("a new state is at the current version and is empty", func(t *testing.T) {
		s := domain.NewState()

		if s.Version != domain.StateVersion {
			t.Errorf("expected version %d, got %d", domain.StateVersion, s.Version)
		}
		if len(s.Files) != 0 || len(s.Entities) != 0 {
			t.Errorf("expected an empty state, got %d files and %d entities", len(s.Files), len(s.Entities))
		}
	})

	t.Run("it reports a tracked row by _id", func(t *testing.T) {
		s := domain.NewState()
		s.Track("admin", row("users.yaml", "users", 7, 0))

		got, ok := s.Row("admin")
		if !ok {
			t.Fatal("expected admin to be tracked")
		}
		if got.PKValue != 7 || got.Table != "users" {
			t.Errorf("expected users row 7, got %+v", got)
		}

		if _, ok := s.Row("nobody"); ok {
			t.Error("expected an untracked _id to report false")
		}
	})

	t.Run("forgetting an _id leaves the rest alone", func(t *testing.T) {
		s := domain.NewState()
		s.Track("admin", row("users.yaml", "users", 7, 0))
		s.Track("guest", row("users.yaml", "users", 8, 1))

		s.Forget("admin")

		if _, ok := s.Row("admin"); ok {
			t.Error("expected admin to be forgotten")
		}
		if _, ok := s.Row("guest"); !ok {
			t.Error("expected guest to survive")
		}
	})

	t.Run("an untracked file and a file with no hash are different answers", func(t *testing.T) {
		s := domain.NewState()
		s.TrackFile("legacy.yaml", "")

		// A file synced before content hashing existed is tracked with no
		// hash. Reporting that as untracked would make sync insert its graph a
		// second time.
		hash, tracked := s.FileHash("legacy.yaml")
		if !tracked || hash != "" {
			t.Errorf("expected tracked with an empty hash, got tracked=%v hash=%q", tracked, hash)
		}

		if _, tracked := s.FileHash("never.yaml"); tracked {
			t.Error("expected an unsynced file to report untracked")
		}
	})

	t.Run("it returns a file's rows in the order they were written", func(t *testing.T) {
		s := domain.NewState()
		s.Track("third", row("a.yaml", "profiles", 3, 2))
		s.Track("first", row("a.yaml", "users", 1, 0))
		s.Track("second", row("a.yaml", "profiles", 2, 1))
		s.Track("elsewhere", row("b.yaml", "users", 9, 0))

		got := s.RowsInFile("a.yaml")

		if len(got) != 3 {
			t.Fatalf("expected 3 rows from a.yaml, got %d", len(got))
		}
		for i, want := range []string{"first", "second", "third"} {
			if got[i].RefID != want {
				t.Errorf("position %d: expected %q, got %q", i, want, got[i].RefID)
			}
		}
	})

	t.Run("it orders rows that share a position by _id", func(t *testing.T) {
		// Only a dirty file's rows are re-numbered, so an entity left behind by
		// an edit can share a position with one that moved into it. Without a
		// final key the order of those two changes between runs on the same
		// data.
		s := domain.NewState()
		s.Track("zeta", row("a.yaml", "users", 2, 1))
		s.Track("alpha", row("a.yaml", "users", 3, 1))

		first := s.RowsInFile("a.yaml")
		second := s.RowsInFile("a.yaml")

		if first[0].RefID != "alpha" || first[1].RefID != "zeta" {
			t.Errorf("expected alpha before zeta, got %q then %q", first[0].RefID, first[1].RefID)
		}
		if first[0].RefID != second[0].RefID {
			t.Error("expected two reads of the same state to agree")
		}
	})

	t.Run("AllRows spans every file, ordered by file", func(t *testing.T) {
		s := domain.NewState()
		s.Track("b", row("b.yaml", "users", 9, 0))
		s.Track("a", row("a.yaml", "users", 1, 0))

		got := s.AllRows()

		if len(got) != 2 {
			t.Fatalf("expected 2 rows, got %d", len(got))
		}
		if got[0].RefID != "a" || got[1].RefID != "b" {
			t.Errorf("expected a then b, got %q then %q", got[0].RefID, got[1].RefID)
		}
	})

	t.Run("a file's rows include the ones with no _id", func(t *testing.T) {
		// An unkeyed row is still that file's row: forget has to drop it, the
		// diff has to show it, and a count of what the file tracks that left it
		// out would be wrong. Only identity matching skips it, and that reads
		// Entities directly.
		s := domain.NewState()
		s.Track("keyed", row("a.yaml", "users", 1, 0))
		s.Unkeyed = []domain.TrackedRow{
			{EntityFile: "a.yaml", TableName: "users", RowPK: 99, PKColumn: "id", InsertionOrder: 1},
			{EntityFile: "b.yaml", TableName: "users", RowPK: 98, PKColumn: "id"},
		}

		got := s.RowsInFile("a.yaml")

		if len(got) != 2 {
			t.Fatalf("expected 2 rows from a.yaml, got %d", len(got))
		}
		if got[0].RefID != "keyed" || got[1].RefID != "" {
			t.Errorf("expected the keyed row then the unkeyed one, got %q then %q", got[0].RefID, got[1].RefID)
		}

		if len(s.AllRows()) != 3 {
			t.Errorf("expected AllRows to span both files and both kinds, got %d", len(s.AllRows()))
		}
	})
}
