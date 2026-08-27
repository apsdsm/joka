package status

import (
	"io"

	"github.com/apsdsm/joka/internal/textui"
)

// The report tables are laid out by internal/textui, which measures column
// widths in runes — the ✓ and · glyphs are three bytes each, so a byte-width
// pad would shift every column after them.
const (
	alignLeft  = textui.AlignLeft
	alignRight = textui.AlignRight
)

// newTable builds a report table whose rows carry a severity rather than a
// colour, so the section builders stay out of the colour vocabulary.
func newTable(headers ...string) *reportTable {
	return &reportTable{inner: textui.NewTable(headers...)}
}

type reportTable struct {
	inner *textui.Table
}

func (t *reportTable) align(aligns ...textui.Alignment) {
	t.inner.Align(aligns...)
}

func (t *reportTable) add(sev severity, notes []string, cells ...string) {
	t.inner.Add(sev.color(), notes, cells...)
}

func (t *reportTable) render(w io.Writer) {
	t.inner.Render(w)
}

// pad brings s up to width runes.
func pad(s string, width int, a textui.Alignment) string {
	return textui.Pad(s, width, a)
}
