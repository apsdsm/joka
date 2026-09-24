package shared

import (
	"errors"
	"fmt"
)

// Exit statuses.
//
// The useful contract for a CI gate is three-valued: nothing to do, could not
// tell you, and there is work. joka had two, so a pipeline could not tell a
// database that needs migrating from a joka that could not reach it — and the
// only signal for "there are changes" was whichever command happened to refuse,
// which is why `entity sync --on-conflict=fail` ended up being read as a drift
// gate rather than as the refusal it is.
const (
	// ExitOK is nothing to do.
	ExitOK = 0
	// ExitError is joka could not do it, or refused to.
	ExitError = 1
	// ExitPending is there is work: migrations to apply, seeds to change,
	// schema that has drifted.
	ExitPending = 2
)

// ErrChangesPending marks an error as "there is work to do" rather than
// "something went wrong", which is what makes the exit status ExitPending.
//
// Wrap it when the message is worth printing — `migrate verify`'s drift does
// that. Return ErrPendingReported when the command has already printed the
// detail itself.
var ErrChangesPending = errors.New("changes pending")

// ErrPendingReported is ExitPending with nothing more to say: the command has
// printed the plan, the status or the diff already, and repeating it under
// `Error:` would say it twice — and, under --output json, would print an error
// object after a document that was not an error.
var ErrPendingReported = fmt.Errorf("%w: reported above", ErrChangesPending)

// ExitCodeFor maps a command's error onto a process exit status.
func ExitCodeFor(err error) int {
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, ErrChangesPending):
		return ExitPending
	}

	return ExitError
}

// AlreadyReported says whether the command printed everything it had to say,
// so main adds nothing. Declining a prompt and reporting pending work are both
// complete without a second line.
func AlreadyReported(err error) bool {
	return errors.Is(err, ErrCancelled) || errors.Is(err, ErrPendingReported)
}
