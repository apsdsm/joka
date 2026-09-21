package entity

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/apsdsm/joka/internal/domains/entity/app"
	"github.com/fatih/color"
)

// askConflicts walks the conflicts and asks which side is right.
//
// The answer is per column, but the question is asked in bulk first: a drifted
// database usually drifted in one direction for one reason, and making someone
// answer forty times to say so is how a useful prompt becomes a thing people
// pipe `yes` into.
//
// Returns nil, false when the operator backed out.
func askConflicts(conflicts []app.RowConflict) ([]app.Resolution, bool) {
	return askConflictsFrom(os.Stdin, conflicts)
}

// askConflictsFrom is askConflicts with the answers coming from anywhere, so
// the decision logic can be tested without a terminal.
func askConflictsFrom(answers io.Reader, conflicts []app.RowConflict) ([]app.Resolution, bool) {
	in := bufio.NewReader(answers)

	columns := 0
	for _, row := range conflicts {
		columns += len(row.Columns)
	}

	fmt.Println()
	color.Set(color.Bold)
	fmt.Printf("%d %s changed in the database since joka last wrote.\n",
		columns, plural(columns, "column", "columns"))
	color.Unset()

	for {
		fmt.Println()
		fmt.Println("  [f] the file is right — write it over the database")
		fmt.Println("  [d] the database is right — keep it, and update the seed files to match")
		fmt.Println("  [r] review each one")
		fmt.Println("  [q] cancel, change nothing")
		fmt.Print("\nWhich? ")

		switch readAnswer(in) {
		case "f":
			return resolveAll(conflicts, false), true
		case "d":
			return resolveAll(conflicts, true), true
		case "r":
			return reviewEach(in, conflicts)
		case "q":
			return nil, false
		}
	}
}

// resolveAll answers every column the same way.
func resolveAll(conflicts []app.RowConflict, database bool) []app.Resolution {
	var out []app.Resolution

	for _, row := range conflicts {
		for _, c := range row.Columns {
			out = append(out, app.Resolution{
				File: row.File, RefID: row.RefID, Column: c.Column, Value: c.Before,
				KeepDatabase: database,
				// Keeping the database's value and rewriting the declaration
				// are separate: a templated column can have the first without
				// the second.
				UpdateFile: database && app.Writable(c),
			})
		}
	}

	return out
}

// reviewEach asks per column.
func reviewEach(in *bufio.Reader, conflicts []app.RowConflict) ([]app.Resolution, bool) {
	var out []app.Resolution

	red := color.New(color.FgRed)
	green := color.New(color.FgGreen)

	for _, row := range conflicts {
		for _, c := range row.Columns {
			fmt.Println()
			color.Yellow("  %s  %s %s %d  (%s)", row.RefID, row.Table, row.PKColumn, row.PKValue, row.File)
			fmt.Printf("    %s\n", c.Column)

			if c.Regenerated {
				// No values: a fresh hash says nothing, and a secret must not
				// be printed.
				red.Println("      the database holds a value joka did not write")
			} else {
				red.Printf("      database  %s\n", c.Before)
				green.Printf("      file      %s\n", c.After)
			}

			if !app.Writable(c) {
				fmt.Println("      (the declaration is an expression, so it cannot be rewritten to match)")
			}

			fmt.Print("\n    [f] file  [d] database  [q] cancel: ")

			switch readAnswer(in) {
			case "f":
				out = append(out, app.Resolution{
					File: row.File, RefID: row.RefID, Column: c.Column,
				})
			case "d":
				out = append(out, app.Resolution{
					File: row.File, RefID: row.RefID, Column: c.Column, Value: c.Before,
					KeepDatabase: true,
					UpdateFile:   app.Writable(c),
				})
			case "q":
				return nil, false
			default:
				// Anything else re-asks this column rather than guessing.
				return continueReview(in, conflicts, out, row, c)
			}
		}
	}

	return out, true
}

// continueReview re-asks the column that got an unrecognised answer, then picks
// the walk back up. Split out so reviewEach stays a plain loop.
func continueReview(
	in *bufio.Reader,
	conflicts []app.RowConflict,
	done []app.Resolution,
	row app.RowConflict,
	c app.ColumnChange,
) ([]app.Resolution, bool) {
	remaining := after(conflicts, row.RefID, c.Column)

	rest, ok := reviewEach(in, append([]app.RowConflict{{
		File: row.File, RefID: row.RefID, Table: row.Table,
		PKColumn: row.PKColumn, PKValue: row.PKValue,
		Columns: []app.ColumnChange{c},
	}}, remaining...))
	if !ok {
		return nil, false
	}

	return append(done, rest...), true
}

// after returns the conflicts following the given column, so a re-ask does not
// replay the ones already answered.
func after(conflicts []app.RowConflict, refID, column string) []app.RowConflict {
	var out []app.RowConflict
	passed := false

	for _, row := range conflicts {
		var columns []app.ColumnChange
		for _, c := range row.Columns {
			if passed {
				columns = append(columns, c)
				continue
			}
			if row.RefID == refID && c.Column == column {
				passed = true
			}
		}
		if len(columns) > 0 {
			row.Columns = columns
			out = append(out, row)
		}
	}

	return out
}

func readAnswer(in *bufio.Reader) string {
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "q"
	}
	return strings.ToLower(strings.TrimSpace(line))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
