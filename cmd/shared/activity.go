package shared

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Activity says what joka is doing while it does it.
//
// It exists because a remote run is minutes of work with nothing on the
// screen, and silence is indistinguishable from a hang — which is how `joka
// apply` against a database in another region was first reported. The work was
// fine; the absence of output was not.
//
// On a terminal it animates one line in place. Everywhere else — a pipe, a CI
// log, --output json — it does not animate at all: a spinner rewriting itself
// with carriage returns turns a log file into a single unreadable line, and the
// useful thing there is one plain line per phase.
type Activity struct {
	mu sync.Mutex
	// out is nil when this Activity says nothing at all.
	out io.Writer
	// animate is false for anything that is not a terminal.
	animate bool

	message string
	frame   int
	drawn   bool

	stop chan struct{}
	done chan struct{}
}

// spinner frames. Single-width runes, so the line does not jitter as it turns.
var spinnerFrames = []rune("⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏")

// spinnerInterval is slow enough not to be noise and fast enough to read as
// movement rather than as a stuck character.
const spinnerInterval = 100 * time.Millisecond

// NewActivity returns an Activity writing to w.
//
// Pass quiet for the cases with no human watching — --output json, where the
// contract is one document and nothing else.
func NewActivity(w io.Writer, quiet bool) *Activity {
	if quiet || w == nil {
		return &Activity{}
	}

	return &Activity{out: w, animate: isTerminal(w)}
}

// Progress is the Activity joka uses by default: stderr, so a redirected
// stdout still receives only the command's own output.
func Progress(quiet bool) *Activity { return NewActivity(os.Stderr, quiet) }

// Start begins reporting, and Update changes what is being reported without
// interrupting it. Calling Start twice is the same as Start then Update.
func (a *Activity) Start(message string) {
	if a.out == nil {
		return
	}

	a.mu.Lock()
	already := a.stop != nil
	a.message = message
	a.mu.Unlock()

	if already {
		a.Update(message)
		return
	}

	if !a.animate {
		a.line(message)
		return
	}

	a.stop = make(chan struct{})
	a.done = make(chan struct{})

	go a.spin()
}

// Update changes the message. On a terminal the spinner keeps turning; in a
// log it is one more line.
func (a *Activity) Update(message string) {
	if a.out == nil {
		return
	}

	a.mu.Lock()
	same := a.message == message
	a.message = message
	a.mu.Unlock()

	if !a.animate && !same {
		a.line(message)
	}
}

// Stop ends the report and leaves the line clear, so whatever prints next
// starts from a clean column rather than over a half-drawn spinner.
func (a *Activity) Stop() {
	if a.out == nil || !a.animate {
		return
	}

	a.mu.Lock()
	running := a.stop != nil
	a.mu.Unlock()

	if !running {
		return
	}

	close(a.stop)
	<-a.done

	a.mu.Lock()
	defer a.mu.Unlock()
	a.clear()
	a.stop, a.done = nil, nil
}

// spin redraws until told to stop.
func (a *Activity) spin() {
	defer close(a.done)

	ticker := time.NewTicker(spinnerInterval)
	defer ticker.Stop()

	a.draw()
	for {
		select {
		case <-a.stop:
			return
		case <-ticker.C:
			a.draw()
		}
	}
}

func (a *Activity) draw() {
	a.mu.Lock()
	defer a.mu.Unlock()

	frame := spinnerFrames[a.frame%len(spinnerFrames)]
	a.frame++

	a.clear()
	fmt.Fprintf(a.out, "%c %s", frame, a.message)
	a.drawn = true
}

// clear erases the drawn line. The spaces matter: a shorter message after a
// longer one would otherwise leave the tail of the old one behind.
func (a *Activity) clear() {
	if !a.drawn {
		return
	}

	fmt.Fprintf(a.out, "\r%s\r", strings.Repeat(" ", len(a.message)+2))
	a.drawn = false
}

// line writes one plain line, for a log.
func (a *Activity) line(message string) {
	a.mu.Lock()
	defer a.mu.Unlock()

	fmt.Fprintf(a.out, "%s\n", message)
}

// isTerminal reports whether w is something a person is watching.
//
// A character device is the test, which needs no dependency and is right for
// the case that matters: a pipe, a file and a CI log are all not one.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}

	info, err := f.Stat()

	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// Count renders a number with its noun, so "1 entity" reads correctly and a
// progress line does not say "1 entities".
func Count(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}

	return fmt.Sprintf("%d %s", n, plural)
}
