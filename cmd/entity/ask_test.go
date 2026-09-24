package entity

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/app"
)

// quiet runs fn with stdout discarded, so the prompt's own printing does not
// bury the test output.
func quiet(t *testing.T, fn func()) {
	t.Helper()

	real := os.Stdout
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("opening %s: %v", os.DevNull, err)
	}
	defer devnull.Close() //nolint:errcheck

	os.Stdout = devnull
	defer func() { os.Stdout = real }()

	fn()
}

// twoConflicts is a plain column and one joka cannot rewrite.
func twoConflicts() []app.RowConflict {
	return []app.RowConflict{{
		File: "a.yaml", RefID: "admin", Table: "users", PKColumn: "id", PKValue: 1,
		Columns: []app.ColumnChange{
			{Column: "email", Before: "moved@example.com", After: "admin@example.com"},
			{Column: "password_hash", Regenerated: true},
		},
	}}
}

func ask(t *testing.T, answers string) ([]app.Resolution, bool) {
	t.Helper()

	var got []app.Resolution
	var ok bool

	quiet(t, func() {
		got, ok = askConflictsFrom(strings.NewReader(answers), twoConflicts())
	})

	return got, ok
}

func TestAskConflicts(t *testing.T) {
	t.Run("the file wins everything", func(t *testing.T) {
		got, ok := ask(t, "f\n")
		if !ok {
			t.Fatal("expected the run to proceed")
		}
		if len(got) != 2 {
			t.Fatalf("expected both columns answered, got %d", len(got))
		}
		for _, r := range got {
			if r.KeepDatabase || r.UpdateFile {
				t.Errorf("expected nothing conceded, got %+v", r)
			}
		}
	})

	t.Run("the database wins everything, and only the writable column is rewritten", func(t *testing.T) {
		got, ok := ask(t, "d\n")
		if !ok {
			t.Fatal("expected the run to proceed")
		}

		by := byColumn(got)

		// Keeping the database's value and rewriting the declaration are
		// separate decisions.
		if !by["email"].KeepDatabase || !by["email"].UpdateFile {
			t.Errorf("expected the literal kept and rewritten, got %+v", by["email"])
		}
		if !by["password_hash"].KeepDatabase || by["password_hash"].UpdateFile {
			t.Errorf("expected the regenerated column kept but not rewritten, got %+v", by["password_hash"])
		}
		if by["email"].Value != "moved@example.com" {
			t.Errorf("expected the database's value carried, got %q", by["email"].Value)
		}
	})

	t.Run("cancelling changes nothing", func(t *testing.T) {
		got, ok := ask(t, "q\n")
		if ok {
			t.Error("expected the run cancelled")
		}
		if got != nil {
			t.Errorf("expected no resolutions, got %+v", got)
		}
	})

	t.Run("an unrecognised bulk answer re-asks rather than guessing", func(t *testing.T) {
		got, ok := ask(t, "maybe\nf\n")
		if !ok || len(got) != 2 {
			t.Fatalf("expected the second answer taken, got %+v ok=%v", got, ok)
		}
	})

	t.Run("reviewing answers each column on its own", func(t *testing.T) {
		got, ok := ask(t, "r\nd\nf\n")
		if !ok {
			t.Fatal("expected the run to proceed")
		}

		by := byColumn(got)
		if !by["email"].KeepDatabase {
			t.Errorf("expected email conceded, got %+v", by["email"])
		}
		if by["password_hash"].KeepDatabase {
			t.Errorf("expected password_hash to go to the file, got %+v", by["password_hash"])
		}
	})

	t.Run("cancelling part-way through a review changes nothing", func(t *testing.T) {
		got, ok := ask(t, "r\nd\nq\n")
		if ok {
			t.Error("expected the run cancelled")
		}
		if got != nil {
			t.Errorf("expected the earlier answer discarded too, got %+v", got)
		}
	})

	t.Run("an unrecognised review answer re-asks that column and keeps going", func(t *testing.T) {
		got, ok := ask(t, "r\nwhat\nd\nf\n")
		if !ok {
			t.Fatal("expected the run to proceed")
		}
		if len(got) != 2 {
			t.Fatalf("expected both columns answered exactly once, got %+v", got)
		}

		by := byColumn(got)
		if !by["email"].KeepDatabase {
			t.Errorf("expected the re-asked column answered, got %+v", by["email"])
		}
		if by["password_hash"].KeepDatabase {
			t.Errorf("expected the next column still asked, got %+v", by["password_hash"])
		}
	})

	t.Run("an empty answer stream cancels rather than looping", func(t *testing.T) {
		var ok bool
		quiet(t, func() {
			_, ok = askConflictsFrom(strings.NewReader(""), twoConflicts())
		})
		if ok {
			t.Error("expected EOF to cancel")
		}
	})
}

func byColumn(resolutions []app.Resolution) map[string]app.Resolution {
	out := make(map[string]app.Resolution, len(resolutions))
	for _, r := range resolutions {
		out[r.Column] = r
	}
	return out
}

var _ io.Reader = strings.NewReader("")
