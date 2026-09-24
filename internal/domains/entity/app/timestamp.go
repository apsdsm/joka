package app

import (
	"strings"
	"time"
)

// timestampLayouts are the spellings joka treats as an instant, narrowest
// intent first. A value that parses in none of them is left exactly as it is.
//
// The set is deliberately short. Anything vaguer — a bare year, a time with no
// date — would start canonicalising strings nobody meant as timestamps.
var timestampLayouts = []string{
	"2006-01-02 15:04:05.999999999 -0700 MST", // how Go prints a time.Time
	"2006-01-02T15:04:05.999999999Z07:00",     // RFC3339, any precision
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// canonicalTimestamp renders a value that denotes an instant in one form, and
// reports whether it was one.
//
// This is the fix for a bug that made every timestamp column look like drift
// for ever. joka stores the hash of the string it inserted — "2026-09-24
// 05:49:13" — and reads the value back through the driver as a time.Time,
// which %v renders as "2026-09-24 05:49:13 +0000 UTC". The two never matched,
// so live-against-baseline always said the database had moved, on a row nobody
// had touched. It hit literal timestamps as readily as {{ now }}, and the only
// way round it was to mark every timestamp column _once.
//
// Both sides go through this, so the declared string and the driver's value
// land on the same text. Two spellings of one instant then compare equal —
// "2026-01-01" and "2026-01-01 00:00:00" both become the same string — which
// is the intended reading of a column that holds an instant, and the cost of
// having no column type to consult here.
func canonicalTimestamp(s string) (string, bool) {
	// Cheap reject first: every layout needs a date, and this runs on every
	// column of every row.
	if len(s) < len("2006-01-02") || strings.Count(s[:10], "-") != 2 {
		return "", false
	}

	for _, layout := range timestampLayouts {
		t, err := time.Parse(layout, s)
		if err != nil {
			continue
		}

		return t.UTC().Format(time.RFC3339Nano), true
	}

	return "", false
}
