package entity

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/fatih/color"
	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/domains/entity/app"
	"github.com/apsdsm/joka/internal/domains/entity/domain"
	"github.com/apsdsm/joka/internal/domains/entity/infra"
)

// RunEntityStatusCommand handles the "entity status" command.
type RunEntityStatusCommand struct {
	DB          *sql.DB
	Driver      jokadb.Driver
	EntitiesDir string
}

func (r RunEntityStatusCommand) Execute(ctx context.Context) error {
	dbAdapter := newEntityAdapter(r.Driver, r.DB)

	if err := dbAdapter.EnsureTrackingTable(ctx); err != nil {
		return fmt.Errorf("ensuring tracking table: %w", err)
	}

	if err := dbAdapter.EnsureContentHashColumn(ctx); err != nil {
		return fmt.Errorf("ensuring content hash column: %w", err)
	}

	relPaths, err := infra.DiscoverEntityFiles(r.EntitiesDir)
	if err != nil {
		return err
	}

	results, err := app.EntityStatusAction{
		DB:          dbAdapter,
		EntitiesDir: r.EntitiesDir,
		Files:       relPaths,
	}.Execute(ctx)
	if err != nil {
		return err
	}

	if len(results) == 0 {
		color.Yellow("No entity files found.")
		return nil
	}

	fmt.Println()
	color.Set(color.Bold)
	fmt.Println("Entity file status:")
	color.Unset()

	for _, info := range results {
		switch info.Status {
		case domain.StatusSynced:
			color.Green("  [synced]    %s", info.Path)
		case domain.StatusModified:
			color.Yellow("  [modified]  %s", info.Path)
		case domain.StatusNew:
			color.Cyan("  [new]       %s", info.Path)
		case domain.StatusOrphaned:
			color.Red("  [orphaned]  %s", info.Path)
		}
	}

	fmt.Println()
	return nil
}
