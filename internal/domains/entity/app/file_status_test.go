package app

import (
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

func TestFileStatusFor(t *testing.T) {
	const hash = "abc123"

	t.Run("it reports an untracked file as new", func(t *testing.T) {
		if got := FileStatusFor(false, "", hash); got != domain.StatusNew {
			t.Errorf("expected new, got %q", got)
		}
	})

	t.Run("it reports a matching stored hash as synced", func(t *testing.T) {
		if got := FileStatusFor(true, hash, hash); got != domain.StatusSynced {
			t.Errorf("expected synced, got %q", got)
		}
	})

	t.Run("it reports a differing stored hash as modified", func(t *testing.T) {
		if got := FileStatusFor(true, "stale", hash); got != domain.StatusModified {
			t.Errorf("expected modified, got %q", got)
		}
	})

	t.Run("it reports an empty stored hash as modified", func(t *testing.T) {
		// A row written before content hashing existed has nothing to compare
		// against. Reading it as synced would strand it: nothing would ever
		// rewrite it, so it would never get a hash.
		if got := FileStatusFor(true, "", hash); got != domain.StatusModified {
			t.Errorf("expected modified, got %q", got)
		}
	})

	t.Run("it does not treat an untracked file with a stored hash as anything but new", func(t *testing.T) {
		// tracked is the authority on whether joka_entities has a row. A hash
		// read out of a map that has no entry comes back as the zero value, and
		// the caller must not have to guard that separately.
		if got := FileStatusFor(false, hash, hash); got != domain.StatusNew {
			t.Errorf("expected new, got %q", got)
		}
	})
}
