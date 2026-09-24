package shared

import (
	"errors"
	"fmt"
	"testing"
)

func TestExitCodeFor(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"success is 0", nil, ExitOK},
		{"an ordinary error is 1", errors.New("could not connect"), ExitError},
		{"a declined step is 1, not pending work", ErrCancelled, ExitError},
		{"reported pending work is 2", ErrPendingReported, ExitPending},
		{"a wrapped pending error is 2", fmt.Errorf("%w: schema drift detected", ErrChangesPending), ExitPending},
		{"pending survives further wrapping", fmt.Errorf("verify: %w", ErrPendingReported), ExitPending},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ExitCodeFor(c.err); got != c.want {
				t.Errorf("expected %d, got %d", c.want, got)
			}
		})
	}
}

func TestAlreadyReported(t *testing.T) {
	t.Run("a declined step has said its piece", func(t *testing.T) {
		if !AlreadyReported(ErrCancelled) {
			t.Error("expected ErrCancelled to be already reported")
		}
	})

	t.Run("reported pending work has said its piece", func(t *testing.T) {
		// This is what stops a dry run under --output json printing an error
		// object after a document that was not an error.
		if !AlreadyReported(ErrPendingReported) {
			t.Error("expected ErrPendingReported to be already reported")
		}
	})

	t.Run("pending work with a message of its own is not", func(t *testing.T) {
		// migrate verify's drift names the migration it drifted from, and that
		// sentence is worth printing.
		drift := fmt.Errorf("%w: schema drift detected", ErrChangesPending)
		if AlreadyReported(drift) {
			t.Error("expected a wrapped ErrChangesPending still to be printed")
		}
	})

	t.Run("an ordinary error is not", func(t *testing.T) {
		if AlreadyReported(errors.New("could not connect")) {
			t.Error("expected an ordinary error to be printed")
		}
	})
}
