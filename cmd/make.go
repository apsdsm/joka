package cmd

import (
	"context"

	"github.com/fatih/color"
	"github.com/nickfiggins/joka/internal/domains/migration/infra"
)

// RunMakeCommand handles the "make" command to create a new migration file.
type RunMakeCommand struct {
	MigrationsDir string
	Name          string
}

// Execute creates a new migration file with the specified name in the migrations directory.
func (r RunMakeCommand) Execute(c context.Context) error {
	color.Green("Creating new migration file '%s' in '%s'...", r.Name, r.MigrationsDir)

	filename, err := infra.CreateMigrationFile(r.MigrationsDir, r.Name)
	if err != nil {
		color.Red("Error: %v", err)
		return err
	}

	color.Green("Created migration file: %s", filename)
	return nil
}
