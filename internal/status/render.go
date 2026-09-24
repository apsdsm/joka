package status

import (
	"fmt"
	"io"
	"strings"

	"github.com/fatih/color"
)

// Render writes the report as five fixed lines plus whatever needs attention.
//
// The sections are always in the same order and always present, so the output
// reads the same shape every time and the eye can go straight to the line it
// wants. A section with nothing wrong says so in one line rather than going
// quiet, because a missing line is indistinguishable from a section that failed.
func Render(w io.Writer, r *Report) {
	bold := color.New(color.Bold)
	dim := color.New(color.Faint)
	yellow := color.New(color.FgYellow)
	green := color.New(color.FgGreen)

	fmt.Fprintln(w)

	bold.Fprintf(w, "%-11s", "database")
	fmt.Fprintln(w, describeDatabase(r))

	bold.Fprintf(w, "%-11s", "state")
	if r.StateFile.NeedsAttention {
		yellow.Fprintln(w, r.StateFile.Note)
	} else {
		fmt.Fprintln(w, r.StateFile.Note)
	}
	dim.Fprintf(w, "           %s\n", r.StateFile.Path)

	bold.Fprintf(w, "%-11s", "migrations")
	fmt.Fprintln(w, describeMigrations(r.Migrations))

	bold.Fprintf(w, "%-11s", "entities")
	fmt.Fprintln(w, describeEntities(r.Entities))
	dim.Fprintf(w, "           %s\n", r.Entities.Dir)

	bold.Fprintf(w, "%-11s", "lock")
	if r.Lock == nil {
		green.Fprintln(w, "free")
	} else {
		yellow.Fprintf(w, "held by %s since %s (%s)\n",
			r.Lock.LockedBy, r.Lock.LockedAt, r.Lock.Operation)
	}

	fmt.Fprintln(w)
}

func describeDatabase(r *Report) string {
	if !r.Meta.Present {
		return "no joka bookkeeping here yet"
	}

	parts := []string{fmt.Sprintf("tracking v%d", r.Meta.TrackingVersion)}
	if r.Meta.JokaVersion != "" {
		parts = append(parts, "written by joka "+r.Meta.JokaVersion)
	}
	// Who owns the database is the first thing worth knowing when a write was
	// refused, and status is the command you reach for after a refusal.
	if r.Meta.StateRoot != "" {
		parts = append(parts, "root "+r.Meta.StateRoot)
	}
	if r.Profile != "" {
		parts = append(parts, "profile "+r.Profile)
	}
	return strings.Join(parts, " · ")
}

func describeMigrations(m Migrations) string {
	if m.Problem != "" {
		return "could not be read — " + m.Problem
	}
	if !m.Tracked {
		return "no joka_migrations table — run 'joka init'"
	}

	parts := []string{fmt.Sprintf("%d applied", m.Applied)}
	if m.Pending > 0 {
		parts = append(parts, fmt.Sprintf("%d pending", m.Pending))
	}

	switch {
	case m.Drift > 0:
		parts = append(parts, fmt.Sprintf("%s drifted from the snapshot — 'joka migrate verify' shows which",
			countOf(m.Drift, "table", "tables")))
	case m.DriftChecked:
		parts = append(parts, "no drift")
	case m.Snapshots == 0:
		parts = append(parts, "no snapshots")
	}

	return strings.Join(parts, " · ")
}

func describeEntities(e Entities) string {
	if e.Problem != "" {
		return "could not be read — " + e.Problem
	}

	parts := []string{fmt.Sprintf("%d tracked across %s",
		e.Tracked, countOf(e.Files, "file", "files"))}

	if e.Declared != e.Tracked {
		parts = append(parts, fmt.Sprintf("%d declared on disk — 'joka entity sync --dry-run' shows the difference",
			e.Declared))
	}
	if e.Unkeyed > 0 {
		parts = append(parts, fmt.Sprintf("%s with no _id", countOf(e.Unkeyed, "row", "rows")))
	}

	return strings.Join(parts, " · ")
}

func countOf(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}
