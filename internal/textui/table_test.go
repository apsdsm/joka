package textui

import (
	"bytes"
	"strings"
	"testing"
)

func render(t *testing.T, build func(*Table)) []string {
	t.Helper()
	table := NewTable("name", "mark", "note")
	table.HeaderStyle = nil
	build(table)

	var buf bytes.Buffer
	table.Render(&buf)
	return strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
}

// runeOffset returns where substr starts, counted in runes. Byte offsets will
// not do: ✓ is three bytes and · is two, so two correctly aligned columns sit
// at different byte offsets — which is the whole reason this package measures
// in runes.
func runeOffset(s, substr string) int {
	i := strings.Index(s, substr)
	if i < 0 {
		return -1
	}
	return Width(s[:i])
}

func TestTableAlignsMultiByteGlyphs(t *testing.T) {
	// ✓ is three bytes, · is two. Padding by byte length would leave the
	// column after them ragged, and by different amounts per glyph.
	lines := render(t, func(table *Table) {
		table.Add(nil, nil, "short", "✓", "ok")
		table.Add(nil, nil, "a_much_longer_name", "·", "pending")
	})

	if len(lines) != 3 {
		t.Fatalf("expected a header and two rows, got %d lines: %q", len(lines), lines)
	}

	first := runeOffset(lines[1], "ok")
	second := runeOffset(lines[2], "pending")
	if first != second {
		t.Errorf("expected the third column to line up, got rune offsets %d and %d\n%s",
			first, second, strings.Join(lines, "\n"))
	}
}
func TestTableRightAlign(t *testing.T) {
	table := NewTable("n")
	table.HeaderStyle = nil
	table.Align(AlignRight)
	table.Add(nil, nil, "1", "one")
	table.Add(nil, nil, "100", "hundred")

	var buf bytes.Buffer
	table.Render(&buf)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")

	if !strings.Contains(lines[1], "    1  one") {
		t.Errorf("expected 1 right-aligned under 100, got %q", lines[1])
	}
}

func TestTableRendersNotesUnderTheirRow(t *testing.T) {
	lines := render(t, func(table *Table) {
		table.Add(nil, []string{"first note", "second note"}, "a", "✓", "")
		table.Add(nil, nil, "b", "·", "")
	})

	if len(lines) != 5 {
		t.Fatalf("expected header, row, two notes and a row, got %q", lines)
	}
	if !strings.Contains(lines[2], "first note") || !strings.Contains(lines[3], "second note") {
		t.Errorf("expected the notes under their row, got %q", lines)
	}
	if !strings.HasPrefix(lines[2], strings.Repeat(" ", table0NoteIndent)) {
		t.Errorf("expected notes indented past the row, got %q", lines[2])
	}
}

// table0NoteIndent mirrors NewTable's default, which the test asserts against.
const table0NoteIndent = 6

func TestTableRendersNothingWithoutRows(t *testing.T) {
	table := NewTable("a", "b")

	var buf bytes.Buffer
	table.Render(&buf)

	if buf.Len() != 0 {
		t.Errorf("expected no output for an empty table, got %q", buf.String())
	}
}

func TestPad(t *testing.T) {
	if got := Pad("✓", 3, AlignLeft); got != "✓  " {
		t.Errorf("expected two trailing spaces after a one-rune glyph, got %q", got)
	}
	if got := Pad("✓", 3, AlignRight); got != "  ✓" {
		t.Errorf("expected two leading spaces, got %q", got)
	}
	if got := Pad("toolong", 3, AlignLeft); got != "toolong" {
		t.Errorf("expected an over-wide cell to pass through, got %q", got)
	}
}
