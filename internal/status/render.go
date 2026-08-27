package status

import (
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	entityapp "github.com/apsdsm/joka/internal/domains/entity/app"
	entitydomain "github.com/apsdsm/joka/internal/domains/entity/domain"
	"github.com/apsdsm/joka/internal/meta"
	"github.com/fatih/color"
)

// Glyphs for the declared/tracked columns. One rune each, so column widths can
// be computed by rune count.
const (
	glyphPresent = "✓"
	glyphAbsent  = "·"
)

// severity picks a row's colour. The three planes are the structure of the
// report; severity is only about how much attention a row wants.
type severity int

const (
	sevOK severity = iota
	sevInfo
	sevWarn
	sevBad
)

func (s severity) color() *color.Color {
	switch s {
	case sevInfo:
		return color.New(color.FgCyan)
	case sevWarn:
		return color.New(color.FgYellow)
	case sevBad:
		return color.New(color.FgRed)
	default:
		return color.New(color.FgGreen)
	}
}

// RenderText writes the report as an aligned text report.
func RenderText(w io.Writer, r Report) {
	bold := color.New(color.Bold)
	dim := color.New(color.Faint)

	fmt.Fprintln(w)
	bold.Fprintf(w, "joka status")
	fmt.Fprintf(w, "   %s\n", strings.Join(nonEmpty(r.Driver, profileLabel(r.Profile), writtenByLabel(r.Meta)), " · "))
	dim.Fprintln(w, "declared = the devops folder · tracked = joka_* tables · live = the database")
	fmt.Fprintln(w)

	renderMigrations(w, r.Migrations)
	renderEntities(w, r.Entities)
	renderTemplates(w, r.Templates)
	renderLock(w, r.Lock)
	renderActions(w, r.Actions)

	fmt.Fprintln(w)
}

func renderMigrations(w io.Writer, m Migrations) {
	section(w, "MIGRATIONS", m.Dir)

	if m.Skipped != "" {
		note(w, m.Skipped, sevWarn)
	}

	if len(m.Files) > 0 {
		t := newTable("migration", "declared", "tracked", "status")
		t.align(alignLeft, alignRight, alignRight, alignLeft)

		for _, f := range m.Files {
			label := f.Index
			if f.Name != "" {
				label += "_" + f.Name
			}

			sev := sevOK
			switch f.Status {
			case MigrationPending:
				sev = sevInfo
			case MigrationOutOfOrder, MigrationFileMissing:
				sev = sevBad
			}

			t.add(sev, nil, label, glyph(f.Declared), glyph(f.Tracked), statusWord(f.Status))
		}

		t.render(w)
		summary(w, counts(
			count{m.Applied, "applied"},
			count{m.Pending, "pending"},
			count{m.OutOfOrder, "out of order"},
			count{m.FileMissing, "file missing"},
		))
	}

	renderDrift(w, m.Drift)
}

func renderDrift(w io.Writer, d Drift) {
	fmt.Fprintln(w)

	if d.Skipped != "" {
		subsection(w, "schema drift")
		note(w, "not checked: "+d.Skipped, sevInfo)
		return
	}

	if !d.HasDrift() {
		subsection(w, "schema drift vs snapshot "+d.SnapshotIndex)
		note(w, "no drift", sevOK)
		return
	}

	subsection(w, fmt.Sprintf("schema drift vs snapshot %s — %s",
		d.SnapshotIndex, plural(d.Count(), "table differs", "tables differ")))

	red := sevBad.color()

	for _, table := range d.Added {
		red.Fprintf(w, "    + %s\n", table)
		note2(w, "in live, not in the snapshot — DDL applied outside a migration", sevBad)
	}
	for _, table := range d.Removed {
		red.Fprintf(w, "    - %s\n", table)
		note2(w, "in the snapshot, not in live", sevBad)
	}
	for _, table := range d.Modified {
		red.Fprintf(w, "    ~ %s\n", table.Table)
		for _, line := range firstN(table.OnlyInLive, 3) {
			note2(w, "live only:     "+line, sevBad)
		}
		for _, line := range firstN(table.OnlyInSnapshot, 3) {
			note2(w, "snapshot only: "+line, sevBad)
		}
		if extra := len(table.OnlyInLive) + len(table.OnlyInSnapshot) - 6; extra > 0 {
			note2(w, fmt.Sprintf("… %d more lines differ — joka migrate verify prints both statements", extra), sevBad)
		}
	}
}

func renderEntities(w io.Writer, e Entities) {
	fmt.Fprintln(w)
	section(w, "ENTITIES", e.Dir)

	if e.Skipped != "" {
		note(w, e.Skipped, sevWarn)
	}

	if len(e.Files) == 0 {
		return
	}

	t := newTable("file", "declared", "tracked", "live", "status")
	t.align(alignLeft, alignRight, alignRight, alignRight, alignLeft)

	for _, f := range e.Files {
		sev := sevOK
		switch f.Status {
		case string(entitydomain.StatusNew):
			sev = sevInfo
		case string(entitydomain.StatusModified):
			sev = sevWarn
		case string(entitydomain.StatusOrphaned):
			sev = sevBad
		}
		if f.Structural != "" || f.MissingRows > 0 || f.ParseError != "" || len(f.IdentityProblems) > 0 {
			sev = sevBad
		}

		var notes []string
		if f.ParseError != "" {
			notes = append(notes, f.ParseError)
		}

		for _, problem := range f.IdentityProblems {
			notes = append(notes, identityNote(problem))
		}
		if f.Structural != "" {
			notes = append(notes, "sync would refuse: "+trimStructuralPrefix(f.Structural))
			if f.KeyedByID {
				notes = append(notes, "every declared entity and tracked row carries an _id")
			}
		}
		if f.MissingRows > 0 {
			notes = append(notes, fmt.Sprintf("%s tracked for this file %s not in the database",
				plural(f.MissingRows, "row", "rows"), isAre(f.MissingRows)))
		}
		for _, table := range f.MissingTables {
			notes = append(notes, fmt.Sprintf("table %s no longer exists", table))
		}

		t.add(sev, notes, f.Path,
			optionalCount(f.Declared),
			optionalCount(f.Tracked),
			optionalCount(f.Live),
			statusWord(f.Status))
	}

	t.render(w)
	summary(w, counts(
		count{e.Counts.Synced, "synced"},
		count{e.Counts.Modified, "modified"},
		count{e.Counts.New, "new"},
		count{e.Counts.Orphaned, "orphaned"},
	))
}

func renderTemplates(w io.Writer, tpl Templates) {
	fmt.Fprintln(w)
	section(w, "TEMPLATES", tpl.Dir)

	if tpl.Skipped != "" {
		note(w, tpl.Skipped, sevInfo)
		return
	}

	t := newTable("table", "strategy", "files", "declared", "live", "")
	t.align(alignLeft, alignLeft, alignRight, alignRight, alignRight, alignLeft)

	for _, table := range tpl.Tables {
		sev := sevOK
		trailing := ""

		switch {
		case table.TableMissing:
			sev = sevBad
			trailing = "table does not exist"
		case table.LoadError != "":
			sev = sevBad
		case table.Declared != table.Live:
			// Only truncate makes the counts equal by definition.
			if table.Strategy == "truncate" {
				sev = sevWarn
				trailing = "differs"
			} else {
				trailing = table.Strategy + " strategy — the counts need not match"
			}
		}

		var notes []string
		if table.LoadError != "" {
			notes = append(notes, table.LoadError)
		}

		t.add(sev, notes, table.Name, table.Strategy,
			fmt.Sprint(table.Files),
			fmt.Sprint(table.Declared),
			optionalCount(table.Live),
			trailing)
	}

	t.render(w)
}

func renderLock(w io.Writer, l *Lock) {
	fmt.Fprintln(w)
	section(w, "LOCK", "")

	if l == nil {
		note(w, "not held", sevOK)
		return
	}

	note(w, fmt.Sprintf("%q held by %s since %s (%s)", l.Operation, l.LockedBy, l.LockedAt, l.Age), sevWarn)
}

func renderActions(w io.Writer, actions []Action) {
	fmt.Fprintln(w)
	section(w, "ACTIONS", "")

	if len(actions) == 0 {
		note(w, "nothing to do", sevOK)
		return
	}

	width := 0
	for _, a := range actions {
		width = max(width, utf8.RuneCountInString(a.Scope))
	}

	for _, a := range actions {
		sev := sevWarn
		command := a.Command
		if command == "" {
			// Naming the gap is the point: a finding with no command is one
			// joka cannot currently fix.
			command = "(nothing joka can run)"
			sev = sevBad
		}

		sev.color().Fprintf(w, "  %s  %s\n", pad(a.Scope, width, alignLeft), command)

		reason := trimStructuralPrefix(a.Reason)
		// The subject is only worth repeating when neither the command nor the
		// reason already names it.
		if a.Subject != "" && !strings.Contains(command, a.Subject) && !strings.Contains(reason, a.Subject) {
			reason = a.Subject + ": " + reason
		}
		note2(w, reason, sev)
	}
}

// --- small helpers ---------------------------------------------------------

func section(w io.Writer, name, dir string) {
	color.New(color.Bold).Fprintf(w, "%s", name)
	if dir != "" {
		fmt.Fprintf(w, "  %s", dir)
	}
	fmt.Fprintln(w)
}

func subsection(w io.Writer, text string) {
	color.New(color.Bold).Fprintf(w, "  %s\n", text)
}

func note(w io.Writer, text string, sev severity) {
	sev.color().Fprintf(w, "  %s\n", text)
}

func note2(w io.Writer, text string, sev severity) {
	sev.color().Fprintf(w, "      %s\n", text)
}

func summary(w io.Writer, text string) {
	if text == "" {
		return
	}
	color.New(color.Faint).Fprintf(w, "  %s\n", text)
}

type count struct {
	n     int
	label string
}

// counts joins the non-zero counts into "3 applied · 1 pending".
func counts(items ...count) string {
	var parts []string
	for _, item := range items {
		if item.n > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", item.n, item.label))
		}
	}
	return strings.Join(parts, " · ")
}

func glyph(present bool) string {
	if present {
		return glyphPresent
	}
	return glyphAbsent
}

// optionalCount renders a count, or the absent glyph when the count does not
// apply at all (-1, e.g. the declared count of a file that is not on disk). A
// real zero renders as 0: "nothing is tracked" and "not applicable" are
// different findings.
func optionalCount(n int) string {
	if n < 0 {
		return glyphAbsent
	}
	return fmt.Sprint(n)
}

func statusWord(status string) string {
	return strings.ReplaceAll(status, "_", " ")
}

func profileLabel(profile string) string {
	if profile == "" {
		return ""
	}
	return "profile " + profile
}

func nonEmpty(values ...string) []string {
	var out []string
	for _, v := range values {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func firstN(values []string, n int) []string {
	if len(values) <= n {
		return values
	}
	return values[:n]
}

// trimStructuralPrefix drops the sentinel that prefixes every structural-change
// message. Status keeps the message verbatim in the report (both it and sync
// get it from the same check, and JSON consumers want what sync would print),
// but the sentinel names the remedy, which the report already names in the
// actions list.
func trimStructuralPrefix(message string) string {
	return strings.TrimPrefix(message, entitydomain.ErrStructuralChange.Error()+": ")
}

// writtenByLabel names the joka that last wrote this database's bookkeeping.
// Empty when nothing has: a database with tracking tables but no joka_meta
// predates the marker, which is not worth a line of its own.
func writtenByLabel(m meta.State) string {
	if !m.Present || m.JokaVersion == "" {
		return ""
	}
	return "written by joka " + m.JokaVersion
}

// identityNote describes one thing standing between a file and identity-keyed
// tracking.
func identityNote(p entityapp.EntitySetProblem) string {
	if p.Kind == entityapp.ProblemMissingID {
		return fmt.Sprintf("entity #%d (%s) has no _id", p.Where[0].Position, p.Where[0].Table)
	}

	others := make([]string, 0, len(p.Where))
	for _, loc := range p.Where {
		others = append(others, fmt.Sprintf("%s #%d", loc.File, loc.Position))
	}
	return fmt.Sprintf("_id %q is claimed by %s", p.RefID, strings.Join(others, " and "))
}
