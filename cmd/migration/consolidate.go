package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fatih/color"

	"github.com/apsdsm/joka/cmd/shared"
	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/domains/migration/app"
	"github.com/apsdsm/joka/internal/domains/migration/domain"
	"github.com/apsdsm/joka/internal/domains/migration/infra"
)

// RunConsolidateCommand handles "migrate consolidate --up-to <index>". It
// replaces all migration files up to and including the target with a single
// file containing the schema snapshot at that point, with tables ordered to
// respect foreign key dependencies.
//
// Nothing destructive happens until the generated SQL has been proven
// applicable: the schema is checked for objects snapshots cannot represent, and
// (on PostgreSQL) the generated file is applied to a throwaway schema inside a
// rolled-back transaction. The tracking tables are reconciled with the files
// so the database consolidate ran against can still migrate afterwards.
type RunConsolidateCommand struct {
	DB               *sql.DB
	Driver           jokadb.Driver
	MigrationsDir    string
	UpToIndex        string
	AutoConfirm      bool
	AllowUnsupported bool
	OutputFormat     string
}

// Execute performs the consolidation.
func (r RunConsolidateCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON

	fail := func(err error) error {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		color.Red("Error: %v", err)
		return err
	}

	adapter := newMigrationAdapter(r.Driver, r.DB)

	// 1. Build the migration chain.
	chain, err := app.GetMigrationChainAction{
		DB:            adapter,
		MigrationsDir: r.MigrationsDir,
	}.Execute(ctx)
	if err != nil {
		return fail(err)
	}

	// 2. Find the target migration and validate everything up to it is applied.
	targetIdx := -1
	for i, m := range chain {
		if m.MigrationIndex == r.UpToIndex {
			targetIdx = i
			break
		}
	}
	if targetIdx < 0 {
		return fail(fmt.Errorf("migration %s not found in chain", r.UpToIndex))
	}

	for i := 0; i <= targetIdx; i++ {
		if chain[i].Status != domain.StatusApplied {
			return fail(fmt.Errorf("migration %s is not applied — all migrations up to %s must be applied before consolidating", chain[i].MigrationIndex, r.UpToIndex))
		}
	}

	// Must have at least 2 migrations to consolidate.
	if targetIdx < 1 {
		return fail(fmt.Errorf("need at least 2 migrations to consolidate (found %d)", targetIdx+1))
	}

	// 3. Refuse if the schema holds objects a snapshot cannot represent. The
	// snapshot only knows about tables, so consolidating a schema with views,
	// enum types, functions or triggers would produce a baseline that is
	// quietly missing them.
	unsupported, err := adapter.UnsupportedSchemaObjects(ctx)
	if err != nil {
		return fail(err)
	}
	if len(unsupported) > 0 && !r.AllowUnsupported {
		return fail(fmt.Errorf("%w:\n  - %s\n\nMigration snapshots capture tables only, so a consolidated baseline would not recreate these.\nRe-run with --allow-unsupported if they are created by migrations that are not being consolidated,\nor build the baseline by hand (e.g. pg_dump --schema-only)",
			domain.ErrUnsupportedSchemaObjects, joinLines(unsupported)))
	}

	// 4. Fetch the schema snapshot for the target migration.
	snapshotJSON, err := adapter.GetSchemaSnapshot(ctx, r.UpToIndex)
	if err != nil {
		return fail(err)
	}

	var schema map[string]string
	if err := json.Unmarshal([]byte(snapshotJSON), &schema); err != nil {
		return fail(fmt.Errorf("parsing snapshot: %w", err))
	}

	// 5. Topologically sort tables by FK dependencies.
	deps := app.ParseFKDependencies(schema)
	order, err := app.TopologicalSort(deps)
	if err != nil {
		return fail(err)
	}

	consolidatedSQL := app.GenerateConsolidatedSQL(schema, order)

	// 6. Prove the generated SQL actually applies, before anything is written
	// or deleted. This is the check whose absence let a broken file replace the
	// migrations that produced it.
	validated := true
	if err := adapter.ValidateSchemaSQL(ctx, consolidatedSQL); err != nil {
		if !errors.Is(err, domain.ErrSchemaValidationUnsupported) {
			return fail(err)
		}
		validated = false
	}

	// 7. Show what will happen and confirm.
	filesToDelete := chain[:targetIdx+1]
	newFileName := fmt.Sprintf("%s_consolidated.sql", r.UpToIndex)
	newFilePath := filepath.Join(r.MigrationsDir, newFileName)

	// The target migration keeps its record — the new file carries its index.
	// Everything before it is replaced and must leave the tracking tables, or
	// the chain (zipped positionally against files) breaks on the next command.
	var recordsToRemove []string
	for _, m := range chain[:targetIdx] {
		recordsToRemove = append(recordsToRemove, m.MigrationIndex)
	}

	if !jsonOut {
		color.Green("Consolidation plan:")
		fmt.Printf("  Target: %s\n", r.UpToIndex)
		fmt.Printf("  Migrations to consolidate: %d\n", len(filesToDelete))
		for _, m := range filesToDelete {
			fmt.Printf("    - %s_%s.sql\n", m.MigrationIndex, m.FileName)
		}
		fmt.Printf("  Tables in snapshot: %d\n", len(schema))
		for _, name := range order {
			fmt.Printf("    - %s\n", name)
		}
		fmt.Printf("  New file: %s\n", newFileName)
		fmt.Printf("  Migration records to remove: %d\n", len(recordsToRemove))
		if validated {
			fmt.Println("  Generated SQL: verified applicable")
		} else {
			color.Yellow("  Generated SQL: NOT verified (this driver cannot dry-run DDL)")
		}
		if len(unsupported) > 0 {
			color.Yellow("  Objects the baseline will NOT recreate:\n  - %s", joinLines(unsupported))
		}
		fmt.Println()
	}

	if !r.AutoConfirm && !jsonOut {
		if !shared.Confirm("Proceed with consolidation? This will delete the original migration files. (only 'yes' will proceed): ") {
			fmt.Println("Consolidation aborted by user.")
			return nil
		}
	}

	// 8. Write the consolidated file.
	if err := os.WriteFile(newFilePath, []byte(consolidatedSQL), 0644); err != nil {
		return fail(fmt.Errorf("writing consolidated file: %w", err))
	}

	// 9. Reconcile the tracking tables. Done before deleting files so a failure
	// here leaves the migrations directory intact and the database unchanged.
	if err := adapter.RemoveMigrationRecords(ctx, recordsToRemove); err != nil {
		os.Remove(newFilePath) //nolint:errcheck — best-effort rollback of step 8
		return fail(fmt.Errorf("reconciling migration records: %w", err))
	}

	// 10. Delete the migration files the new file replaces.
	var deleted []string
	for _, m := range filesToDelete {
		// The target file may already be the consolidated file we just wrote.
		if m.FileFullPath == newFilePath {
			deleted = append(deleted, m.MigrationIndex)
			continue
		}
		if err := os.Remove(m.FileFullPath); err != nil {
			return fail(fmt.Errorf("deleting %s: %w — records were already reconciled; remove the remaining consolidated files by hand", m.FileFullPath, err))
		}
		deleted = append(deleted, m.MigrationIndex)
	}

	// 11. Report on the resulting migration directory.
	remaining, err := infra.ListMigrationFiles(r.MigrationsDir)
	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		color.Red("Warning: could not verify migration directory: %v", err)
		return nil
	}

	if jsonOut {
		shared.PrintJSON(map[string]any{
			"status":          "ok",
			"consolidated":    deleted,
			"new_file":        newFileName,
			"total_files":     len(remaining),
			"records_removed": recordsToRemove,
			"validated":       validated,
			"unsupported":     unsupported,
		})
		return nil
	}

	color.Green("Consolidation complete.")
	fmt.Printf("  Created: %s\n", newFileName)
	fmt.Printf("  Deleted: %d migration files\n", len(deleted))
	fmt.Printf("  Removed: %d migration records\n", len(recordsToRemove))
	fmt.Printf("  Remaining migration files: %d\n", len(remaining))
	return nil
}

// joinLines renders a list for a multi-line bullet block.
func joinLines(items []string) string {
	out := ""
	for i, item := range items {
		if i > 0 {
			out += "\n  - "
		}
		out += item
	}
	return out
}
