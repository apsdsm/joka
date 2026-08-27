package status

import (
	"fmt"
	"io"
	"strings"

	entitydomain "github.com/apsdsm/joka/internal/domains/entity/domain"
	templatedomain "github.com/apsdsm/joka/internal/domains/template/domain"
	"github.com/fatih/color"
)

// RenderCompact writes the report as a single line, for CI logs, shell prompts
// and the head of a startup chain — the places where the full report is too
// much but "am I behind?" is worth answering.
//
// The segments are positionally stable: every section always appears, showing
// its counts rather than being omitted when it is healthy, so the line can be
// read at a glance in the same place every time. The exception is the lock,
// which is only there when one is held.
func RenderCompact(w io.Writer, r Report) {
	segments := []string{
		"migrations " + compactMigrations(r.Migrations),
		"drift " + compactDrift(r.Migrations.Drift),
		"entities " + compactEntities(r.Entities),
		"templates " + compactTemplates(r.Templates),
	}

	if r.Lock != nil {
		segments = append(segments, "lock held")
	}

	line := "joka  " + strings.Join(segments, "  ")

	style := color.New(color.FgGreen)
	verdict := "→ ok"
	if len(r.Actions) > 0 {
		style = color.New(color.FgYellow)
		verdict = fmt.Sprintf("→ %s", plural(len(r.Actions), "action", "actions"))
	}

	style.Fprintf(w, "%s  %s\n", line, verdict)
}

// compactMigrations renders applied/declared, with a trailing count of
// migrations that are neither (out of order, or applied with no file).
func compactMigrations(m Migrations) string {
	if m.Skipped != "" && len(m.Files) == 0 {
		return "n/a"
	}

	declared := 0
	for _, f := range m.Files {
		if f.Declared {
			declared++
		}
	}

	out := fmt.Sprintf("%d/%d", m.Applied, declared)
	if broken := m.OutOfOrder + m.FileMissing; broken > 0 {
		out += fmt.Sprintf(" !%d", broken)
	}
	return out
}

func compactDrift(d Drift) string {
	if !d.Checked {
		return "n/a"
	}
	return fmt.Sprint(d.Count())
}

// compactEntities renders synced/total, with trailing counts for the states
// that need attention: +N new or modified, !N with a real problem.
func compactEntities(e Entities) string {
	if len(e.Files) == 0 {
		return "n/a"
	}

	out := fmt.Sprintf("%d/%d", e.Counts.Synced, len(e.Files))

	pending := e.Counts.New + e.Counts.Modified
	if pending > 0 {
		out += fmt.Sprintf(" +%d", pending)
	}

	broken := e.Counts.Orphaned
	for _, f := range e.Files {
		// An orphan is already counted; do not count it twice.
		if f.Status == string(entitydomain.StatusOrphaned) {
			continue
		}
		if f.Structural != "" || f.MissingRows > 0 || f.ParseError != "" {
			broken++
		}
	}
	if broken > 0 {
		out += fmt.Sprintf(" !%d", broken)
	}

	return out
}

// compactTemplates renders agreeing/total, counting a table as agreeing unless
// it is missing, unreadable, or a truncate table whose row count differs.
func compactTemplates(t Templates) string {
	if len(t.Tables) == 0 {
		return "n/a"
	}

	ok := 0
	for _, table := range t.Tables {
		switch {
		case table.TableMissing, table.LoadError != "":
		case table.Strategy == string(templatedomain.StrategyTruncate) && table.Declared != table.Live:
		default:
			ok++
		}
	}

	return fmt.Sprintf("%d/%d", ok, len(t.Tables))
}
