package app

import (
	"testing"
	"time"
)

// TestTimestampBaselineSurvivesARoundTrip is the bug jjc2 hit: joka hashes the
// string it inserted and reads the value back as a time.Time, so every
// timestamp column read as drift on every run, for ever.
func TestTimestampBaselineSurvivesARoundTrip(t *testing.T) {
	cases := []struct {
		name     string
		declared string
		live     any
	}{
		{"a {{ now }} timestamp", "2026-09-24 05:49:13",
			time.Date(2026, 9, 24, 5, 49, 13, 0, time.UTC)},
		{"a literal timestamp", "2026-01-15 09:30:00",
			time.Date(2026, 1, 15, 9, 30, 0, 0, time.UTC)},
		{"a date-only column", "2026-01-15",
			time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)},
		{"an RFC3339 declaration", "2026-01-15T09:30:00Z",
			time.Date(2026, 1, 15, 9, 30, 0, 0, time.UTC)},
		{"a non-UTC live value is the same instant", "2026-01-15 09:30:00",
			time.Date(2026, 1, 15, 18, 30, 0, 0, time.FixedZone("JST", 9*3600))},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if HashValue(c.declared) != HashValue(c.live) {
				t.Errorf("declared %q and live %v hash differently:\n  %s\n  %s",
					c.declared, c.live, normalizeValue(c.declared), normalizeValue(c.live))
			}
		})
	}
}

func TestTimestampCanonicalisationLeavesOtherValuesAlone(t *testing.T) {
	for _, s := range []string{"hello", "12345", "3.50", "", "not-a-date", "abc-de-fg"} {
		if _, ok := canonicalTimestamp(s); ok {
			t.Errorf("expected %q not to be read as a timestamp", s)
		}
	}
}

func TestTimestampsThatDifferStillDiffer(t *testing.T) {
	a := HashValue("2026-01-15 09:30:00")
	b := HashValue(time.Date(2026, 1, 15, 9, 30, 1, 0, time.UTC))
	if a == b {
		t.Error("expected a one-second difference to be visible")
	}
}
