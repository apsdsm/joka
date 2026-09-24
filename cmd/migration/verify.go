package migration

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/apsdsm/joka/cmd/shared"
	"github.com/apsdsm/joka/internal/domains/migration/app"
	"github.com/apsdsm/joka/internal/domains/migration/infra"
	"github.com/fatih/color"
)

// ErrSchemaDrift is returned when the live schema differs from the latest
// snapshot. The error is surfaced so callers (CI, scripts) can detect drift
// via exit code.
// ErrSchemaDrift wraps shared.ErrChangesPending, so drift exits 2 and a joka
// that could not run the check exits 1. Both are non-zero, so a CI gate testing
// for failure is unaffected; one that wants to tell "the schema moved" from
// "verify could not tell me" now can.
var ErrSchemaDrift = fmt.Errorf("%w: schema drift detected", shared.ErrChangesPending)

// RunVerifyCommand handles "migrate verify". It compares the live database
// schema against the latest snapshot and reports added/removed/modified
// tables. Exits non-zero when drift is found.
type RunVerifyCommand struct {
	DB           *sql.DB
	OutputFormat string
}

func (r RunVerifyCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON
	adapter := infra.NewPostgresDBAdapter(r.DB)

	result, err := app.VerifySchemaAction{DB: adapter}.Execute(ctx)
	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		color.Red("Error: %v", err)
		return err
	}

	if jsonOut {
		shared.PrintJSON(map[string]any{
			"status":          "ok",
			"migration_index": result.MigrationIndex,
			"drift":           result.HasDrift(),
			"added":           result.Added,
			"removed":         result.Removed,
			"modified":        result.Modified,
		})
		if result.HasDrift() {
			return ErrSchemaDrift
		}
		return nil
	}

	if !result.HasDrift() {
		color.Green("No drift detected against migration %s.", result.MigrationIndex)
		return nil
	}

	color.Red("Schema drift detected against migration %s.", result.MigrationIndex)
	fmt.Println()

	if len(result.Added) > 0 {
		color.Set(color.Bold)
		fmt.Println("Added tables (in live, not in snapshot):")
		color.Unset()
		for _, t := range result.Added {
			color.Green("  + %s", t)
		}
		fmt.Println()
	}

	if len(result.Removed) > 0 {
		color.Set(color.Bold)
		fmt.Println("Removed tables (in snapshot, missing from live):")
		color.Unset()
		for _, t := range result.Removed {
			color.Red("  - %s", t)
		}
		fmt.Println()
	}

	if len(result.Modified) > 0 {
		color.Set(color.Bold)
		fmt.Println("Modified tables:")
		color.Unset()
		for _, m := range result.Modified {
			color.Yellow("  ~ %s", m.Table)
			fmt.Println()
			color.Cyan("    -- snapshot")
			fmt.Println("   ", indent(m.Snapshot))
			fmt.Println()
			color.Cyan("    -- live")
			fmt.Println("   ", indent(m.Live))
			fmt.Println()
		}
	}

	return ErrSchemaDrift
}

// indent prefixes every line after the first with four spaces, so multi-line
// CREATE TABLE output aligns under the "    -- live" / "    -- snapshot"
// headers.
func indent(s string) string {
	out := make([]byte, 0, len(s)+16)
	for i, c := range s {
		if c == '\n' && i != len(s)-1 {
			out = append(out, '\n', ' ', ' ', ' ', ' ')
			continue
		}
		out = append(out, byte(c))
	}
	return string(out)
}
