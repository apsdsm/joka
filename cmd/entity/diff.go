package entity

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/apsdsm/joka/cmd/shared"
	"github.com/apsdsm/joka/internal/domains/entity/app"
	"github.com/apsdsm/joka/internal/domains/entity/infra"
	"github.com/apsdsm/joka/internal/textui"
	"github.com/fatih/color"
)

// RunEntityDiffCommand handles "entity diff". It lines an entity file's
// declared graph up against the rows joka tracks for it and the rows that are
// really in the database, and says whether an _id-keyed match would be exact.
//
// Read-only: it acquires no lock and writes nothing, including the tracking
// tables (a file that has never been synced is something to report).
type RunEntityDiffCommand struct {
	DB          *sql.DB
	EntitiesDir string
	FilePath    string
	// SkipValues turns off the per-row column comparison (--no-values).

	SkipValues   bool
	OutputFormat string
}

func (r RunEntityDiffCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON

	fail := func(err error) error {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	dbAdapter := infra.NewPostgresDBAdapter(r.DB)

	fullPath := filepath.Join(r.EntitiesDir, r.FilePath)
	onDisk := true
	if _, err := os.Stat(fullPath); err != nil {
		onDisk = false
	}

	// Tracking is read, never created: a file that has never been synced is a
	// finding, and diff is the command you reach for on a database where
	// something is already wrong. Load returns an empty state for a database
	// with no tracking at all, so "has anything ever been synced" comes from
	// the state rather than from a probe for a particular table.
	//
	// It used to probe joka_entities, which tracking version 3 drops. Every
	// up-to-date database therefore reported every file as never synced, and
	// diff rendered the untracked view for all of them.
	state, err := infra.NewPostgresStateBackend(r.DB).Load(ctx)
	if err != nil {
		return fail(err)
	}

	tracked := len(state.Files) > 0 || len(state.Entities) > 0 || len(state.Unkeyed) > 0

	action := app.DiffEntityAction{
		DB:         dbAdapter,
		State:      state,
		Path:       r.FilePath,
		OnDisk:     onDisk,
		SkipValues: r.SkipValues,
	}

	if onDisk {
		file, err := app.ParseEntityAction{Path: fullPath}.Execute()
		if err != nil {
			return fail(err)
		}
		action.Entities = file.Entities
	}

	if !tracked && !onDisk {
		return fail(fmt.Errorf("%s is neither on disk nor tracked", r.FilePath))
	}

	diff, err := action.Execute(ctx)
	if err != nil {
		return fail(err)
	}

	if jsonOut {
		shared.PrintJSON(struct {
			Status string `json:"status"`
			*app.EntityDiff
		}{Status: "ok", EntityDiff: diff})
		return nil
	}

	renderDiff(os.Stdout, diff)
	return nil
}

// renderDiff writes the alignment.
func renderDiff(w io.Writer, d *app.EntityDiff) {
	bold := color.New(color.Bold)
	dim := color.New(color.Faint)

	fmt.Fprintln(w)
	bold.Fprintf(w, "entity diff  %s\n", d.Path)

	switch {
	case !d.Tracked && d.Adoptions > 0:
		dim.Fprintf(w, "not tracked — %d of %d entities are already in the database and would be claimed\n",
			d.Adoptions, d.DeclaredCount)
	case !d.Tracked:
		dim.Fprintln(w, "not tracked — every entity would be inserted")
	case !d.OnDisk:
		dim.Fprintln(w, "orphaned — the file is gone, only the tracking is left")
	case d.MatchedBy == app.MatchByID:
		dim.Fprintln(w, "matched by _id — every declared entity and tracked row carries one")
	default:
		dim.Fprintln(w, "matched by position — an _id is missing somewhere, so identity matching is not available")
	}

	fmt.Fprintln(w)

	t := textui.NewTable("", "#", "table", "_id", "tracked", "row")
	t.Align(textui.AlignLeft, textui.AlignRight, textui.AlignLeft, textui.AlignLeft, textui.AlignRight, textui.AlignLeft)

	prefixes := treePrefixes(d.Lines)

	for i, line := range d.Lines {
		t.Add(diffStyle(line), diffNotes(line),
			diffMarker(line),
			position(line.DeclaredPos),
			prefixes[i]+diffTable(line),
			orDash(line.RefID),
			position(line.TrackedPos),
			rowLabel(line),
		)
	}

	t.Render(w)

	fmt.Fprintln(w)
	renderDiffSummary(w, d)
	fmt.Fprintln(w)
}

func renderDiffSummary(w io.Writer, d *app.EntityDiff) {
	dim := color.New(color.Faint)

	dim.Fprintf(w, "  %d declared · %d tracked · %d insert · %d claim · %d delete · %d changed · %d moved\n",
		d.DeclaredCount, d.TrackedCount, d.Inserts, d.Adoptions, d.Deletes, d.Changes, d.Moves)

	if len(d.RegeneratedColumns) > 0 {
		dim.Fprintf(w, "  rewritten on every sync regardless of what the row holds: %s\n",
			strings.Join(d.RegeneratedColumns, ", "))
	}

	if len(d.SeededColumns) > 0 {
		dim.Fprintf(w, "  seeded once and owned by the database since: %s\n",
			strings.Join(d.SeededColumns, ", "))
	}

	if d.MissingRows > 0 {
		color.New(color.FgRed).Fprintf(w, "  %d tracked %s not in the database\n",
			d.MissingRows, isAreRows(d.MissingRows))
	}

	if d.PositionalBreak > 0 {
		color.New(color.FgYellow).Fprintf(w, "  positional alignment breaks at declared #%d\n", d.PositionalBreak)
	} else if d.Tracked && d.OnDisk && d.TrackedCount > 0 && d.DeclaredCount > 0 {
		color.New(color.FgGreen).Fprintln(w, "  positional alignment holds all the way through")
	}

	for _, label := range firstFew(d.UnkeyedDeclared, 3) {
		dim.Fprintf(w, "  declared %s has no _id\n", label)
	}
	for _, label := range firstFew(d.UnkeyedTracked, 3) {
		dim.Fprintf(w, "  tracked %s has no ref_id\n", label)
	}

	fmt.Fprintln(w)

	switch {
	case !d.Tracked && d.Adoptions > 0:
		color.New(color.FgCyan).Fprintf(w, "  → joka entity sync   (claims %d, inserts %d)\n",
			d.Adoptions, d.Inserts)
	case !d.Tracked:
		color.New(color.FgCyan).Fprintf(w, "  → joka entity sync   (inserts all %d)\n", d.DeclaredCount)
	case !d.OnDisk:
		color.New(color.FgYellow).Fprintf(w, "  → joka entity forget %s\n", d.Path)
	case d.Changes > 0:
		color.New(color.FgYellow).Fprintf(w, "  → joka entity sync   (updates %d %s in place)\n", d.Changes, isAreRowsNoun(d.Changes))
	case d.MissingRows > 0:
		color.New(color.FgYellow).Fprintf(w, "  → joka entity sync   (puts back the missing %s)\n",
			isAreRowsNoun(d.MissingRows))
		color.New(color.FgYellow).Fprintf(w, "  → joka entity forget %s     (drops the tracking instead, if the deletion was deliberate)\n", d.Path)
	default:
		color.New(color.FgGreen).Fprintln(w, "  the file and the tracking agree")
	}
}

// diffMarker is the left gutter. A changed row reads as changed even when it
// also moved: the two position columns already show the move, while a column
// change is only visible in the notes underneath.
func diffMarker(line app.DiffLine) string {
	switch {
	case line.Status == app.DiffInsert:
		return "+"
	// Distinct from + because the row is already there: sync claims it and
	// writes the declaration over it, rather than creating anything.
	case line.Status == app.DiffAdopt:
		return "@"
	case line.Status == app.DiffDelete:
		return "-"
	case line.Status == app.DiffUnpaired:
		return "!"
	case line.Status == app.DiffChanged:
		return "≠"
	case line.Moved:
		return "~"
	default:
		return "="
	}
}

func diffStyle(line app.DiffLine) *color.Color {
	switch {
	case line.Status == app.DiffInsert:
		return color.New(color.FgCyan)
	case line.Status == app.DiffAdopt:
		return color.New(color.FgYellow)
	case line.Status == app.DiffDelete, line.Status == app.DiffUnpaired:
		return color.New(color.FgRed)
	case line.Status == app.DiffChanged, line.Moved:
		return color.New(color.FgYellow)
	default:
		return color.New(color.FgGreen)
	}
}
func diffNotes(line app.DiffLine) []string {
	var notes []string

	if line.Status == app.DiffAdopt {
		notes = append(notes, fmt.Sprintf(
			"already in the database and not tracked — sync would claim this row, matched on %s",
			strings.Join(line.MatchedOn, ", ")))
	}

	if line.DeclaredTable != "" {
		notes = append(notes, fmt.Sprintf("the file declares table %s here, the tracked row is in %s",
			line.DeclaredTable, line.Table))
	}
	if line.TableMissing {
		notes = append(notes, fmt.Sprintf("table %s no longer exists", line.Table))
	} else if line.TrackedPos > 0 && !line.Live {
		notes = append(notes, "row is not in the database")
	}

	for _, change := range firstChanges(line.Changes, 5) {
		switch {
		case change.Deferred:
			notes = append(notes, fmt.Sprintf("%s  (lookup, resolved at apply time)", change.Column))
		default:
			notes = append(notes, fmt.Sprintf("%s  %s → %s", change.Column,
				truncate(change.Before, 56), truncate(change.After, 56)))
		}
	}
	if extra := len(line.Changes) - 5; extra > 0 {
		notes = append(notes, fmt.Sprintf("… %d more columns differ", extra))
	}

	return notes
}

func diffTable(line app.DiffLine) string {
	if line.DeclaredTable != "" {
		return line.DeclaredTable + " / " + line.Table
	}
	return line.Table
}

func rowLabel(line app.DiffLine) string {
	// An adopted line has no tracked position — that is what makes it an
	// adoption — but it does name the row sync would claim, which is the one
	// thing the reader wants to see on that line.
	if line.Status == app.DiffAdopt {
		return fmt.Sprintf("%s %d", line.PKColumn, line.PKValue)
	}
	if line.TrackedPos == 0 {
		return "·"
	}
	label := fmt.Sprintf("%s %d", line.PKColumn, line.PKValue)
	if line.TableMissing {
		return label + "  no table"
	}
	if !line.Live {
		return label + "  gone"
	}
	return label
}

func position(n int) string {
	if n == 0 {
		return "·"
	}
	return fmt.Sprint(n)
}

func orDash(s string) string {
	if s == "" {
		return "·"
	}
	return s
}

func truncate(s string, n int) string {
	if textui.Width(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n]) + "…"
}

func firstFew(values []string, n int) []string {
	if len(values) <= n {
		return values
	}
	return values[:n]
}

func firstChanges(changes []app.ColumnChange, n int) []app.ColumnChange {
	if len(changes) <= n {
		return changes
	}
	return changes[:n]
}

func isAreRows(n int) string {
	if n == 1 {
		return "row is"
	}
	return "rows are"
}

func isAreRowsNoun(n int) string {
	if n == 1 {
		return "row"
	}
	return "rows"
}

// treePrefixes renders the `_has:` nesting as box-drawing connectors, one
// prefix per line.
//
// Whether a line is the last child at its depth is derived from the depth
// sequence alone: walking forward, the next line at the same depth before any
// shallower line is a following sibling. Ancestors that still have following
// siblings get a continuation bar so deeper levels stay connected.
func treePrefixes(lines []app.DiffLine) []string {
	out := make([]string, len(lines))

	for i, line := range lines {
		if line.Depth == 0 {
			continue
		}

		// hasSibling[d] reports whether the ancestor at depth d has another
		// child after this line.
		hasSibling := make([]bool, line.Depth+1)
		for d := 1; d <= line.Depth; d++ {
			hasSibling[d] = hasFollowingSibling(lines, i, d)
		}

		var b strings.Builder
		for d := 1; d < line.Depth; d++ {
			if hasSibling[d] {
				b.WriteString("│  ")
			} else {
				b.WriteString("   ")
			}
		}
		if hasSibling[line.Depth] {
			b.WriteString("├─ ")
		} else {
			b.WriteString("└─ ")
		}

		out[i] = b.String()
	}

	return out
}

// hasFollowingSibling reports whether another entity appears at the given depth
// after line i without the tree first rising above that depth.
func hasFollowingSibling(lines []app.DiffLine, i, depth int) bool {
	for j := i + 1; j < len(lines); j++ {
		switch {
		case lines[j].Depth < depth:
			return false
		case lines[j].Depth == depth:
			return true
		}
	}
	return false
}
