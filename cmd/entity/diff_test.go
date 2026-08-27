package entity

import (
	"strings"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/app"
)

// tree renders the prefixes for a sequence of nesting depths.
func tree(depths ...int) []string {
	lines := make([]app.DiffLine, len(depths))
	for i, d := range depths {
		lines[i] = app.DiffLine{Depth: d}
	}
	return treePrefixes(lines)
}

func TestTreePrefixes(t *testing.T) {
	t.Run("it leaves a flat list unprefixed", func(t *testing.T) {
		for i, got := range tree(0, 0, 0) {
			if got != "" {
				t.Errorf("line %d: expected no prefix, got %q", i, got)
			}
		}
	})

	t.Run("it closes a lone child", func(t *testing.T) {
		// fields
		//   └─ field_versions
		got := tree(0, 1)
		if got[1] != "└─ " {
			t.Errorf("expected a closing connector, got %q", got[1])
		}
	})

	t.Run("it distinguishes a middle child from the last", func(t *testing.T) {
		// parent
		//   ├─ first
		//   └─ second
		got := tree(0, 1, 1)
		if got[1] != "├─ " {
			t.Errorf("expected a continuing connector on the first child, got %q", got[1])
		}
		if got[2] != "└─ " {
			t.Errorf("expected a closing connector on the last child, got %q", got[2])
		}
	})

	t.Run("it keeps a continuation bar through a grandchild", func(t *testing.T) {
		// parent
		//   ├─ first
		//   │  └─ grandchild
		//   └─ second
		got := tree(0, 1, 2, 1)
		if got[2] != "│  └─ " {
			t.Errorf("expected the ancestor bar carried down, got %q", got[2])
		}
		if got[3] != "└─ " {
			t.Errorf("expected the last child closed, got %q", got[3])
		}
	})

	t.Run("it drops the bar when the ancestor has no more children", func(t *testing.T) {
		// parent
		//   └─ only
		//      └─ grandchild
		got := tree(0, 1, 2)
		if got[1] != "└─ " {
			t.Errorf("expected the child closed, got %q", got[1])
		}
		if got[2] != "   └─ " {
			t.Errorf("expected blank indent under a closed ancestor, got %q", got[2])
		}
	})

	t.Run("it starts a new tree at the next top-level entity", func(t *testing.T) {
		// first          second
		//   └─ child       └─ child
		got := tree(0, 1, 0, 1)
		if got[1] != "└─ " || got[3] != "└─ " {
			t.Errorf("expected each child closed within its own parent, got %q and %q", got[1], got[3])
		}
		if got[2] != "" {
			t.Errorf("expected the second top-level entity unprefixed, got %q", got[2])
		}
	})

	t.Run("it handles three levels", func(t *testing.T) {
		// parent
		//   ├─ child
		//   │  ├─ grandchild
		//   │  └─ grandchild
		//   └─ child
		got := tree(0, 1, 2, 2, 1)
		for i, want := range []string{"", "├─ ", "│  ├─ ", "│  └─ ", "└─ "} {
			if got[i] != want {
				t.Errorf("line %d: expected %q, got %q", i, want, got[i])
			}
		}
	})

	t.Run("its prefixes are all the same display width per depth", func(t *testing.T) {
		// Every connector is one rune plus two, so a depth's cells still line
		// up whichever connector each row got.
		got := tree(0, 1, 1)
		if a, b := len([]rune(got[1])), len([]rune(got[2])); a != b {
			t.Errorf("expected equal widths at the same depth, got %d and %d", a, b)
		}
	})
}

func TestTreePrefixesIgnoresUndeclaredLines(t *testing.T) {
	// A delete has no declared side, so it carries no nesting and sits at the
	// left margin.
	lines := []app.DiffLine{
		{Depth: 0, Status: app.DiffSame, DeclaredPos: 1},
		{Depth: 0, Status: app.DiffDelete, TrackedPos: 2},
	}
	for i, got := range treePrefixes(lines) {
		if got != "" {
			t.Errorf("line %d: expected no prefix, got %q", i, got)
		}
	}
}

func TestTreePrefixesUsedInOutput(t *testing.T) {
	// Guard the wiring: the prefix has to reach the rendered table.
	d := &app.EntityDiff{
		Path: "a.yaml", Tracked: true, OnDisk: true, MatchedBy: app.MatchByID,
		DeclaredCount: 2, TrackedCount: 2,
		Lines: []app.DiffLine{
			{Status: app.DiffSame, DeclaredPos: 1, TrackedPos: 1, Table: "fields", RefID: "a", PKColumn: "id", PKValue: 1, Live: true},
			{Status: app.DiffSame, DeclaredPos: 2, TrackedPos: 2, Table: "field_versions", RefID: "a_v1", PKColumn: "id", PKValue: 1, Live: true, Depth: 1},
		},
	}

	var buf strings.Builder
	renderDiff(&buf, d)

	if !strings.Contains(buf.String(), "└─ field_versions") {
		t.Errorf("expected the child rendered under its parent:\n%s", buf.String())
	}
}
