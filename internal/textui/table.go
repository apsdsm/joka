// Package textui holds the terminal layout helpers shared by joka's report
// commands. Everything here measures width in runes rather than bytes: the
// glyphs the reports use (✓, ·, ≠) are multi-byte, so a byte-width pad shifts
// every column after them.
package textui

import (
	"io"
	"strings"
	"unicode/utf8"

	"github.com/fatih/color"
)

// Alignment controls how a cell is padded within its column.
type Alignment int

const (
	AlignLeft Alignment = iota
	AlignRight
)

// Row is one line of a table plus any detail lines that belong under it.
type Row struct {
	Cells []string
	Notes []string
	// Style colours the whole line. Colour is applied to the assembled line
	// rather than to individual cells because an escape sequence inside a
	// padded cell breaks the alignment it was padded for.
	Style *color.Color
}

// Table lays out rows in aligned columns.
type Table struct {
	headers []string
	aligns  []Alignment
	rows    []Row

	// Indent is the left margin of every line, in spaces.
	Indent int
	// NoteIndent is the left margin of a row's detail lines.
	NoteIndent int
	// HeaderStyle colours the header line.
	HeaderStyle *color.Color
}

// NewTable returns a table with the given column headers. An empty header
// leaves that column unlabelled.
func NewTable(headers ...string) *Table {
	return &Table{
		headers:     headers,
		aligns:      make([]Alignment, len(headers)),
		Indent:      2,
		NoteIndent:  6,
		HeaderStyle: color.New(color.Faint),
	}
}

// Align sets the per-column alignment, left to right. Columns beyond the given
// alignments stay left-aligned.
func (t *Table) Align(aligns ...Alignment) {
	copy(t.aligns, aligns)
}

// Add appends a row. Style may be nil for unstyled output.
func (t *Table) Add(style *color.Color, notes []string, cells ...string) {
	t.rows = append(t.rows, Row{Cells: cells, Notes: notes, Style: style})
}

// Len reports how many rows have been added.
func (t *Table) Len() int { return len(t.rows) }

// Render writes the table. It writes nothing when there are no rows.
func (t *Table) Render(w io.Writer) {
	if len(t.rows) == 0 {
		return
	}

	widths := make([]int, len(t.headers))
	for i, h := range t.headers {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, row := range t.rows {
		for i, cell := range row.Cells {
			if i < len(widths) {
				widths[i] = max(widths[i], utf8.RuneCountInString(cell))
			}
		}
	}

	margin := strings.Repeat(" ", t.Indent)
	noteMargin := strings.Repeat(" ", t.NoteIndent)

	write(w, t.HeaderStyle, margin+t.line(t.headers, widths)+"\n")

	for _, row := range t.rows {
		write(w, row.Style, margin+t.line(row.Cells, widths)+"\n")
		for _, note := range row.Notes {
			write(w, row.Style, noteMargin+note+"\n")
		}
	}
}

// line joins one row's cells at the computed widths. The last column is never
// padded, so a trailing status word leaves no ragged right edge.
func (t *Table) line(cells []string, widths []int) string {
	parts := make([]string, 0, len(cells))
	for i, cell := range cells {
		if i == len(cells)-1 || i >= len(widths) {
			parts = append(parts, cell)
			continue
		}
		parts = append(parts, Pad(cell, widths[i], t.aligns[i]))
	}
	return strings.TrimRight(strings.Join(parts, "  "), " ")
}

// Pad brings s up to width runes.
func Pad(s string, width int, a Alignment) string {
	gap := width - utf8.RuneCountInString(s)
	if gap <= 0 {
		return s
	}
	if a == AlignRight {
		return strings.Repeat(" ", gap) + s
	}
	return s + strings.Repeat(" ", gap)
}

// Width returns the rune count of s — the width it occupies in a terminal
// column, which is what every measurement here is based on.
func Width(s string) int { return utf8.RuneCountInString(s) }

func write(w io.Writer, style *color.Color, s string) {
	if style == nil {
		io.WriteString(w, s) //nolint:errcheck
		return
	}
	style.Fprint(w, s) //nolint:errcheck
}
