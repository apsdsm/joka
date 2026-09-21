package app

import (
	"fmt"
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
	// It does not rewrite the YAML. Updating the declaration to match is what
	// `entity resolve` is for; this only stops joka fighting the database.
	ConflictDB ConflictPolicy = "db"
)

// ParseConflictPolicy reads the --on-conflict value.
func ParseConflictPolicy(s string) (ConflictPolicy, error) {
	switch ConflictPolicy(s) {
	case ConflictFail, ConflictFile, ConflictDB:
		return ConflictPolicy(s), nil
	case "":
		return ConflictFail, nil
	}
	return "", fmt.Errorf("unknown --on-conflict %q: expected fail, file or db", s)
}

// KeepFromConflicts turns a plan's conflicts into the Keep map ApplySetAction
// takes: the columns to leave alone, and the hash of what the database holds
// there.
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
			// Before is the live value, rendered the way HashValue renders,
			// so hashing it here gives the same digest the next comparison
			// will compute from the row itself.
			columns[c.Column] = HashValue(c.Before)
		}
		keep[row.RefID] = columns
	}

	return keep
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
	fmt.Fprintf(&b, "%s changed in the database since joka last wrote %s",
		countOf(columns, "column", "columns"), countOf(len(conflicts), "row", "rows"))

	ordered := make([]RowConflict, len(conflicts))
	copy(ordered, conflicts)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].RefID < ordered[j].RefID })

	for _, row := range ordered {
		fmt.Fprintf(&b, "\n  %s  %s %s %d  (%s)",
			row.RefID, row.Table, row.PKColumn, row.PKValue, row.File)
		for _, c := range row.Columns {
			fmt.Fprintf(&b, "\n    %s: database %q, file %q", c.Column, c.Before, c.After)
		}
	}

	b.WriteString("\n  --on-conflict=file writes the file's values over them;" +
		" --on-conflict=db keeps the database's and stops reporting them")

	return fmt.Errorf("%w: %s", domain.ErrEntityConflict, b.String())
}
