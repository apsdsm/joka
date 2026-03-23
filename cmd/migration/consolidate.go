package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/fatih/color"

	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/cmd/shared"
	"github.com/apsdsm/joka/internal/domains/migration/app"
	"github.com/apsdsm/joka/internal/domains/migration/domain"
	"github.com/apsdsm/joka/internal/domains/migration/infra"
)

// RunConsolidateCommand handles "migrate consolidate --up-to <index>". It
// replaces all migration files up to and including the target with a single
// file containing the schema snapshot at that point, with tables ordered to
// respect foreign key dependencies.
type RunConsolidateCommand struct {
	DB            *sql.DB
	Driver        jokadb.Driver
	MigrationsDir string
	UpToIndex     string
	AutoConfirm   bool
	OutputFormat  string
}

// Execute performs the consolidation.
func (r RunConsolidateCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON

	adapter := newMigrationAdapter(r.Driver, r.DB)

	// 1. Build the migration chain.
	chain, err := app.GetMigrationChainAction{
		DB:            adapter,
		MigrationsDir: r.MigrationsDir,
	}.Execute(ctx)
	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		color.Red("Error: %v", err)
		return err
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
		err := fmt.Errorf("migration %s not found in chain", r.UpToIndex)
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		color.Red("Error: %v", err)
		return err
	}

	for i := 0; i <= targetIdx; i++ {
		if chain[i].Status != domain.StatusApplied {
			err := fmt.Errorf("migration %s is not applied — all migrations up to %s must be applied before consolidating", chain[i].MigrationIndex, r.UpToIndex)
			if jsonOut {
				return shared.PrintErrorJSON(err)
			}
			color.Red("Error: %v", err)
			return err
		}
	}

	// Must have at least 2 migrations to consolidate.
	if targetIdx < 1 {
		err := fmt.Errorf("need at least 2 migrations to consolidate (found %d)", targetIdx+1)
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		color.Red("Error: %v", err)
		return err
	}

	// 3. Fetch the schema snapshot for the target migration.
	snapshotJSON, err := adapter.GetSchemaSnapshot(ctx, r.UpToIndex)
	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		color.Red("Error: %v", err)
		return err
	}

	var schema map[string]string
	if err := json.Unmarshal([]byte(snapshotJSON), &schema); err != nil {
		err = fmt.Errorf("parsing snapshot: %w", err)
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		color.Red("Error: %v", err)
		return err
	}

	// 4. Topologically sort tables by FK dependencies.
	deps := app.ParseFKDependencies(schema)
	order, err := app.TopologicalSort(deps)
	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		color.Red("Error: %v", err)
		return err
	}

	consolidatedSQL := app.GenerateConsolidatedSQL(schema, order)

	// 5. Show what will happen and confirm.
	filesToDelete := chain[:targetIdx+1]
	newFileName := fmt.Sprintf("%s_consolidated.sql", r.UpToIndex)

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
		fmt.Println()
	}

	if !r.AutoConfirm && !jsonOut {
		if !shared.Confirm("Proceed with consolidation? This will delete the original migration files. (only 'yes' will proceed): ") {
			fmt.Println("Consolidation aborted by user.")
			return nil
		}
	}

	// 6. Write the consolidated file.
	newFilePath := filepath.Join(r.MigrationsDir, newFileName)
	if err := os.WriteFile(newFilePath, []byte(consolidatedSQL), 0644); err != nil {
		err = fmt.Errorf("writing consolidated file: %w", err)
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		color.Red("Error: %v", err)
		return err
	}

	// 7. Delete old migration files.
	var deleted []string
	for _, m := range filesToDelete {
		if err := os.Remove(m.FileFullPath); err != nil {
			// Rollback: remove the consolidated file we just wrote.
			os.Remove(newFilePath)
			err = fmt.Errorf("deleting %s: %w", m.FileFullPath, err)
			if jsonOut {
				return shared.PrintErrorJSON(err)
			}
			color.Red("Error: %v", err)
			return err
		}
		deleted = append(deleted, m.MigrationIndex)
	}

	// 8. Verify the resulting migration directory looks correct.
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
			"status":       "ok",
			"consolidated": deleted,
			"new_file":     newFileName,
			"total_files":  len(remaining),
		})
		return nil
	}

	color.Green("Consolidation complete.")
	fmt.Printf("  Created: %s\n", newFileName)
	fmt.Printf("  Deleted: %d migration files\n", len(deleted))
	fmt.Printf("  Remaining migration files: %d\n", len(remaining))
	return nil
}
