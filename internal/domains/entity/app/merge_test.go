package app

import "testing"

func TestClassifyColumn(t *testing.T) {
	const declared = "new"
	const was = "old"

	t.Run("nothing moved", func(t *testing.T) {
		if got := ClassifyColumn("same", "same", HashValue("same"), true); got != VerdictUnchanged {
			t.Errorf("expected unchanged, got %q", got)
		}
	})

	t.Run("the file moved and the database did not", func(t *testing.T) {
		if got := ClassifyColumn(declared, was, HashValue(was), true); got != VerdictPush {
			t.Errorf("expected push, got %q", got)
		}
	})

	t.Run("the database moved and the file did not", func(t *testing.T) {
		if got := ClassifyColumn(was, "changed-by-the-app", HashValue(was), true); got != VerdictConflict {
			t.Errorf("expected conflict, got %q", got)
		}
	})

	t.Run("both moved", func(t *testing.T) {
		if got := ClassifyColumn(declared, "changed-by-the-app", HashValue(was), true); got != VerdictConflict {
			t.Errorf("expected conflict, got %q", got)
		}
	})

	t.Run("no baseline reads as push, not conflict", func(t *testing.T) {
		// The absence of a record is not evidence that the database moved.
		// Treating it as one would make the first sync after an upgrade a wall
		// of conflicts on a database where nothing is wrong.
		if got := ClassifyColumn(declared, was, "", false); got != VerdictPush {
			t.Errorf("expected push, got %q", got)
		}
	})

	t.Run("no baseline and already in agreement is still unchanged", func(t *testing.T) {
		if got := ClassifyColumn("same", "same", "", false); got != VerdictUnchanged {
			t.Errorf("expected unchanged, got %q", got)
		}
	})

	t.Run("a JSON column is compared by meaning, not by rendering", func(t *testing.T) {
		// PostgreSQL renders jsonb in its own key order with a space after each
		// colon. Hashing the raw text would make every JSON column look like
		// drift on every run.
		yaml := `{"b":2,"a":1}`
		postgres := `{"a": 1, "b": 2}`

		if got := ClassifyColumn(yaml, postgres, HashValue(yaml), true); got != VerdictUnchanged {
			t.Errorf("expected unchanged, got %q", got)
		}
	})

	t.Run("a nil live value is a value, not a missing one", func(t *testing.T) {
		// A column set to NULL by the application is a change like any other.
		if got := ClassifyColumn("something", nil, HashValue("something"), true); got != VerdictConflict {
			t.Errorf("expected conflict, got %q", got)
		}
	})
}
