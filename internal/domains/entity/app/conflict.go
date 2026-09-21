package app

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// ConflictPolicy is what `entity sync` does when the database has moved out
// from under the declaration.
type ConflictPolicy string

const (
	// ConflictFail reports the conflicts, writes nothing and exits non-zero.
	// It is the default, and it is what makes `entity sync` a drift gate in
	// CI — the same role `migrate verify` plays for schema.
	ConflictFail ConflictPolicy = "fail"
	// ConflictFile resolves every conflict in the declaration's favour: the
	// file is the desired state, so write it and discard the drift.
	ConflictFile ConflictPolicy = "file"
	// ConflictDB resolves every conflict in the database's favour: leave the
	// column alone and record what it holds as the new baseline, so the same
	// difference is not reported again.
	//
	// It does not rewrite the YAML. Keeping the database's value and updating
	// the declaration to match it are different decisions, and `ask` is where
	// the second one is made.
	ConflictDB ConflictPolicy = "db"
	// ConflictAsk shows each conflict and asks which side is right.
	//
	// This is the mode the whole model exists for: seeds drift in a dozen
	// places, and the useful question when they do is "the database says this,
	// did you mean that?" — with joka updating the seed file when the answer is
	// yes, so the declaration stays true instead of slowly becoming fiction.
	//
	// It needs someone to ask, so it is refused under --output json and with
	// --auto.
	ConflictAsk ConflictPolicy = "ask"
)

// ParseConflictPolicy reads the --on-conflict value.
func ParseConflictPolicy(s string) (ConflictPolicy, error) {
	switch ConflictPolicy(s) {
	case ConflictFail, ConflictFile, ConflictDB, ConflictAsk:
		return ConflictPolicy(s), nil
	case "":
		return ConflictFail, nil
	}
	return "", fmt.Errorf("unknown --on-conflict %q: expected fail, file, db or ask", s)
}

// Resolution is what was decided about one conflicted column.
type Resolution struct {
	File   string
	RefID  string
	Column string
	// Value is what the database holds, for a column the file is to be updated
	// to match. Empty when the file wins.
	Value string
	// KeepDatabase is true when the database's value stands. UpdateFile is true
	// when the declaration is to be rewritten to match it — which is only
	// possible for a literal, so a templated column can be kept without being
	// rewritten.
	KeepDatabase bool
	UpdateFile   bool
}

// Writable reports whether a conflicted column's declaration can be rewritten
// to match the database.
//
// A templated value cannot: writing a literal over `{{ lookup|… }}` would
// replace the indirection with whatever it resolved to this time. A regenerated
// column cannot either — there is no value to write, and for a secret there
// must not be.
func Writable(c ColumnChange) bool {
	return !c.Regenerated && !c.Deferred && !isTemplateString(c.After)
}

// KeepFromConflicts turns a plan's conflicts into the Keep map ApplySetAction
// takes: the columns to leave alone, and the baseline to record for each.
//
// Only ConflictDB produces one. Under ConflictFile the declaration wins and
// every column is written; under ConflictFail the caller has already refused.
func KeepFromConflicts(conflicts []RowConflict, policy ConflictPolicy) map[string]map[string]string {
	if policy != ConflictDB || len(conflicts) == 0 {
		return nil
	}

	keep := make(map[string]map[string]string, len(conflicts))

	for _, row := range conflicts {
		columns := make(map[string]string, len(row.Columns))
		for _, c := range row.Columns {
			columns[c.Column] = adoptedBaseline(c)
		}
		keep[row.RefID] = columns
	}

	return keep
}

// KeepFromResolutions builds the same map from per-column answers.
//
// A column resolved in the database's favour is kept whether or not the file
// could be rewritten to match: keeping the value and updating the declaration
// are separate, and a templated column can have the first without the second.
func KeepFromResolutions(conflicts []RowConflict, resolutions []Resolution) map[string]map[string]string {
	kept := make(map[string]map[string]bool)
	for _, r := range resolutions {
		if !r.KeepDatabase {
			continue
		}
		if kept[r.RefID] == nil {
			kept[r.RefID] = make(map[string]bool)
		}
		kept[r.RefID][r.Column] = true
	}
	if len(kept) == 0 {
		return nil
	}

	keep := make(map[string]map[string]string, len(kept))

	for _, row := range conflicts {
		for _, c := range row.Columns {
			if !kept[row.RefID][c.Column] {
				continue
			}
			if keep[row.RefID] == nil {
				keep[row.RefID] = make(map[string]string)
			}
			keep[row.RefID][c.Column] = adoptedBaseline(c)
		}
	}

	return keep
}

// ApplyResolutions rewrites the seed files for every column resolved in the
// database's favour and marked writable.
//
// entitiesDir is where the declared files live; Resolution.File is relative to
// it, the way every other path in the domain is. It returns the files it
// changed, so the caller can say which ones to look at in a diff.
func ApplyResolutions(entitiesDir string, resolutions []Resolution) ([]string, error) {
	var changed []string
	seen := make(map[string]bool)

	for _, r := range resolutions {
		if !r.UpdateFile {
			continue
		}

		path := filepath.Join(entitiesDir, r.File)
		if err := SetEntityColumn(path, r.RefID, r.Column, r.Value); err != nil {
			return changed, err
		}

		if !seen[r.File] {
			seen[r.File] = true
			changed = append(changed, r.File)
		}
	}

	sort.Strings(changed)
	return changed, nil
}

// ConflictError renders a plan's conflicts into one error wrapping
// ErrEntityConflict, naming every column so the reader can decide without
// running a second command.
func ConflictError(conflicts []RowConflict) error {
	if len(conflicts) == 0 {
		return nil
	}

	columns := 0
	for _, row := range conflicts {
		columns += len(row.Columns)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s in %s",
		countOf(columns, "column", "columns"), countOf(len(conflicts), "row", "rows"))

	ordered := make([]RowConflict, len(conflicts))
	copy(ordered, conflicts)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].RefID < ordered[j].RefID })

	for _, row := range ordered {
		fmt.Fprintf(&b, "\n  %s  %s %s %d  (%s)",
			row.RefID, row.Table, row.PKColumn, row.PKValue, row.File)
		for _, c := range row.Columns {
			if c.Regenerated {
				// A fresh hash tells the reader nothing, and an asm.* secret
				// must not be printed.
				fmt.Fprintf(&b, "\n    %s: the database holds a value joka did not write", c.Column)
				continue
			}
			fmt.Fprintf(&b, "\n    %s: database %q, file %q", c.Column, c.Before, c.After)
		}
	}

	b.WriteString("\n  --on-conflict=file writes the file's values over them;" +
		" --on-conflict=db keeps the database's values and rewrites the seed files to match")

	return fmt.Errorf("%w: %s", domain.ErrEntityConflict, b.String())
}

// FilesToRewrite names the seed files ApplyResolutions would edit, without
// editing them. The confirmation prompt has to say which files it is about to
// change, and it runs before the change.
func FilesToRewrite(resolutions []Resolution) []string {
	var files []string
	seen := make(map[string]bool)

	for _, r := range resolutions {
		if r.UpdateFile && !seen[r.File] {
			seen[r.File] = true
			files = append(files, r.File)
		}
	}

	sort.Strings(files)
	return files
}

// ConflictSummary is ConflictError without the per-column listing: the counts
// and what to do about them.
//
// It exists for the text output, where the plan has already printed every
// column in a layout an error string cannot match, and repeating them under
// `Error:` says the same thing twice. ConflictError is still what JSON and any
// other caller with no plan in front of it gets.
func ConflictSummary(conflicts []RowConflict) error {
	if len(conflicts) == 0 {
		return nil
	}

	columns := 0
	for _, row := range conflicts {
		columns += len(row.Columns)
	}

	return fmt.Errorf("%w: %s in %s (listed above)."+
		" --on-conflict=file writes the file's values over them; --on-conflict=db keeps the"+
		" database's values and rewrites the seed files to match; --on-conflict=ask decides one at a time",
		domain.ErrEntityConflict,
		countOf(columns, "column", "columns"), countOf(len(conflicts), "row", "rows"))
}

// ResolutionsFor renders a non-interactive policy as the per-column answers it
// stands for, so `--on-conflict=db` and answering `d` to every question are the
// same run.
//
// Only ConflictDB produces any. Conceding a column means two things — leave the
// database's value alone, and make the declaration say so — and doing only the
// first does not converge: the file still disagrees with the database, so the
// same conflict is reported on every run from then on. Under ConflictFile the
// declaration already wins and there is nothing to record; ConflictFail refuses
// before this is reached.
func ResolutionsFor(conflicts []RowConflict, policy ConflictPolicy) []Resolution {
	if policy != ConflictDB {
		return nil
	}

	var out []Resolution

	for _, row := range conflicts {
		for _, c := range row.Columns {
			out = append(out, Resolution{
				File: row.File, RefID: row.RefID, Column: c.Column, Value: c.Before,
				KeepDatabase: true,
				// A column whose declaration is an expression is kept without
				// being rewritten: there is no literal to write it to. It stays
				// a conflict, which is the honest answer — joka cannot make the
				// file agree, so it does not pretend the difference is settled.
				UpdateFile: Writable(c),
			})
		}
	}

	return out
}

// adoptedBaseline says what a conceded column records as its baseline: nothing
// when the declaration can be rewritten to match, the database's hash when it
// cannot.
//
// The two cases settle the conflict in different places. A literal is settled
// in the file, where the reader can see it, and the baseline stays the record
// of what joka last applied. A template expression has no literal to write, so
// the only place the concession fits is the baseline — and moving it there is
// safe for exactly these columns, because joka never writes them on the
// strength of a value comparison in the first place.
func adoptedBaseline(c ColumnChange) string {
	if Writable(c) {
		return ""
	}
	return c.LiveHash
}
