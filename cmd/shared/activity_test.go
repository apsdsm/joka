package shared

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestActivityInALog(t *testing.T) {
	// A pipe, a file, a CI log: a spinner rewriting itself with carriage
	// returns turns those into one unreadable line. One line per phase is the
	// useful thing there.
	t.Run("it writes one plain line per phase", func(t *testing.T) {
		var buf bytes.Buffer
		a := NewActivity(&buf, false)

		a.Start("planning the migrations")
		a.Update("planning the seeds")
		a.Stop()

		got := buf.String()
		if strings.Contains(got, "\r") {
			t.Errorf("expected no carriage returns in a log, got %q", got)
		}
		for _, want := range []string{"planning the migrations\n", "planning the seeds\n"} {
			if !strings.Contains(got, want) {
				t.Errorf("expected %q, got %q", want, got)
			}
		}
	})

	t.Run("repeating a message does not repeat the line", func(t *testing.T) {
		var buf bytes.Buffer
		a := NewActivity(&buf, false)

		a.Start("working")
		a.Update("working")
		a.Update("working")
		a.Stop()

		if n := strings.Count(buf.String(), "working"); n != 1 {
			t.Errorf("expected one line, got %d: %q", n, buf.String())
		}
	})
}

func TestActivityQuiet(t *testing.T) {
	// --output json has one contract: one document and nothing else.
	var buf bytes.Buffer
	a := NewActivity(&buf, true)

	a.Start("planning")
	a.Update("still planning")
	a.Stop()

	if buf.Len() != 0 {
		t.Errorf("expected silence, got %q", buf.String())
	}
}

func TestActivityIsSafeToMisuse(t *testing.T) {
	// It is held across error paths, so every order has to be harmless.
	t.Run("Stop without Start", func(t *testing.T) {
		NewActivity(&bytes.Buffer{}, false).Stop()
	})

	t.Run("Stop twice", func(t *testing.T) {
		a := NewActivity(&bytes.Buffer{}, false)
		a.Start("x")
		a.Stop()
		a.Stop()
	})

	t.Run("a nil writer says nothing", func(t *testing.T) {
		a := NewActivity(nil, false)
		a.Start("x")
		a.Update("y")
		a.Stop()
	})
}

// fakeTTY is a writer that claims to be a terminal, so the animated path can
// be exercised without one.
type fakeTTY struct{ bytes.Buffer }

func TestActivityAnimates(t *testing.T) {
	// The animated path is driven through the internals rather than through a
	// real terminal: what matters is that it redraws in place and leaves the
	// line clear, not which frame it was on.
	var buf bytes.Buffer
	a := &Activity{out: &buf, animate: true}

	a.Start("planning the seeds")
	time.Sleep(250 * time.Millisecond)
	a.Stop()

	got := buf.String()
	if !strings.Contains(got, "planning the seeds") {
		t.Errorf("expected the message, got %q", got)
	}
	if !strings.Contains(got, "\r") {
		t.Errorf("expected it to redraw in place, got %q", got)
	}
	if strings.Count(got, "planning the seeds") < 2 {
		t.Errorf("expected more than one frame in 250ms, got %q", got)
	}
	// Whatever prints next must start from a clean column.
	if !strings.HasSuffix(got, "\r") {
		t.Errorf("expected the line to be cleared on stop, got %q", got)
	}
}
