package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/fatih/color"
	"github.com/apsdsm/joka/internal/domains/migration/infra"
)

// RunSnapshotCommand handles "migrate snapshot [migration_index]". It retrieves
// and pretty-prints a stored schema snapshot. If no migration index is given,
// it shows the most recent snapshot.
type RunSnapshotCommand struct {
	DB             *sql.DB
	MigrationIndex string // empty = latest
}

// Execute loads the snapshot from joka_snapshots and prints each table's
// CREATE TABLE statement, sorted alphabetically by table name.
func (r RunSnapshotCommand) Execute(ctx context.Context) error {
	adapter := infra.NewMySQLDBAdapter(r.DB)

	// Resolve which snapshot to show — explicit index or fall back to latest.
	index := r.MigrationIndex
	if index == "" {
		var err error
		index, err = adapter.GetLatestSnapshotIndex(ctx)
		if err != nil {
			color.Red("Error: %v", err)
			return err
		}
	}

	// Fetch the raw JSON snapshot from the database.
	snapshot, err := adapter.GetSchemaSnapshot(ctx, index)
	if err != nil {
		color.Red("Error: %v", err)
		return err
	}

	color.Green("Schema snapshot for migration %s:", index)
	fmt.Println()

	// Parse the JSON map of {table_name: "CREATE TABLE ..."} and print
	// each entry sorted alphabetically.
	var schema map[string]string
	if err := json.Unmarshal([]byte(snapshot), &schema); err != nil {
		// Fall back to printing raw JSON if parsing fails.
		fmt.Println(snapshot)
		return nil
	}

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
