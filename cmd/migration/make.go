package migration

import (
	"context"

	"github.com/fatih/color"
	"github.com/apsdsm/joka/cmd/shared"
	"github.com/apsdsm/joka/internal/domains/migration/infra"
)

// RunMakeCommand handles the "make" command to create a new migration file.
type RunMakeCommand struct {
	MigrationsDir string
	Name          string
	OutputFormat  string
}

// Execute creates a new migration file with the specified name in the migrations directory.
func (r RunMakeCommand) Execute(c context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON

	if !jsonOut {
		color.Green("Creating new migration file '%s' in '%s'...", r.Name, r.MigrationsDir)
	}

	filename, err := infra.CreateMigrationFile(r.MigrationsDir, r.Name)
	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		color.Red("Error: %v", err)
		return err
	}

	if jsonOut {
		shared.PrintJSON(map[string]string{"status": "ok", "file": filename})
		return nil
	}

	color.Green("Created migration file: %s", filename)
	return nil
}
