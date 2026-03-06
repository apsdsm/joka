package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/fatih/color"
	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/cmd/shared"
)

// RunSnapshotCommand handles "migrate snapshot [migration_index]". It retrieves
// and pretty-prints a stored schema snapshot. If no migration index is given,
// it shows the most recent snapshot.
type RunSnapshotCommand struct {
	DB             *sql.DB
	Driver         jokadb.Driver
	MigrationIndex string // empty = latest
	OutputFormat   string
}

// Execute loads the snapshot from joka_snapshots and prints each table's
// CREATE TABLE statement, sorted alphabetically by table name.
func (r RunSnapshotCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON
	adapter := newMigrationAdapter(r.Driver, r.DB)

	// Resolve which snapshot to show — explicit index or fall back to latest.
	index := r.MigrationIndex
	if index == "" {
		var err error
		index, err = adapter.GetLatestSnapshotIndex(ctx)
		if err != nil {
			if jsonOut {
				return shared.PrintErrorJSON(err)
			}
			color.Red("Error: %v", err)
			return err
		}
	}

	// Fetch the raw JSON snapshot from the database.
	snapshot, err := adapter.GetSchemaSnapshot(ctx, index)
	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		color.Red("Error: %v", err)
		return err
	}

	// Parse the JSON map of {table_name: "CREATE TABLE ..."}.
	var schema map[string]string
	if err := json.Unmarshal([]byte(snapshot), &schema); err != nil {
		if jsonOut {
			// Return raw snapshot as a string if parsing fails.
			shared.PrintJSON(map[string]any{"status": "ok", "migration_index": index, "schema_raw": snapshot})
			return nil
		}
		// Fall back to printing raw JSON if parsing fails.
		color.Green("Schema snapshot for migration %s:", index)
		fmt.Println()
		fmt.Println(snapshot)
		return nil
	}

	if jsonOut {
		shared.PrintJSON(map[string]any{"status": "ok", "migration_index": index, "schema": schema})
		return nil
	}

	color.Green("Schema snapshot for migration %s:", index)
	fmt.Println()

	var tables []string
	for name := range schema {
		tables = append(tables, name)
	}
	sort.Strings(tables)

	for _, name := range tables {
		color.Cyan("-- %s", name)
		fmt.Println(schema[name] + ";")
		fmt.Println()
	}

	return nil
}
