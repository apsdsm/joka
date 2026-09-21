package entity

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/apsdsm/joka/cmd/shared"
	"github.com/apsdsm/joka/internal/domains/entity/app"
	"github.com/apsdsm/joka/internal/domains/entity/domain"
	"github.com/apsdsm/joka/internal/domains/entity/infra"
	"github.com/fatih/color"
)

// RunEntityStatusCommand handles the "entity status" command.
type RunEntityStatusCommand struct {
	DB           *sql.DB
	EntitiesDir  string
	OutputFormat string
}

func (r RunEntityStatusCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON

	fail := func(err error) error {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	// Nothing is created: a state that has never been written reads as empty,
	// and every file then reports as new, which is the truth about that
	// database rather than a table this command made on the way past.
	relPaths, err := infra.DiscoverEntityFiles(r.EntitiesDir)
	if err != nil {
		return fail(err)
	}

	state, err := infra.NewPostgresStateBackend(r.DB).Load(ctx)
	if err != nil {
		return fail(err)
	}

	results, err := app.EntityStatusAction{
		State:       state,
		EntitiesDir: r.EntitiesDir,
		Files:       relPaths,
	}.Execute()
	if err != nil {
		return fail(err)
	}

	if jsonOut {
		type fileEntry struct {
			Path   string `json:"path"`
			Status string `json:"status"`
		}
		entries := make([]fileEntry, len(results))
		for i, info := range results {
			entries[i] = fileEntry{Path: info.Path, Status: string(info.Status)}
		}
		shared.PrintJSON(map[string]any{"status": "ok", "files": entries})
		return nil
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
