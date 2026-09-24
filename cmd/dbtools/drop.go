package dbtools

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/apsdsm/joka/cmd/shared"
	"github.com/apsdsm/joka/internal/domains/dbtools/app"
	"github.com/apsdsm/joka/internal/domains/dbtools/infra"
	lockinfra "github.com/apsdsm/joka/internal/domains/lock/infra"
	"github.com/fatih/color"
)

// RunDropCommand drops every user table in the current database/schema,
// including joka_* tracking tables. Destructive — confirms by default.
type RunDropCommand struct {
	DB           *sql.DB
	AutoConfirm  bool
	OutputFormat string
	// SkipLock skips advisory lock acquisition. Used when an outer command
	// (e.g. `joka reset`) already holds the lock.
	SkipLock bool
}

func (r RunDropCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON

	if !r.SkipLock {
		lockAdapter := lockinfra.NewPostgresLockAdapter(r.DB)
		if err := lockAdapter.Acquire(ctx, "drop"); err != nil {
			return err
		}
		defer lockAdapter.Release(ctx) //nolint:errcheck
	}

	dbAdapter := infra.NewPostgresDBAdapter(r.DB)

	tables, err := dbAdapter.ListTables(ctx)
	if err != nil {
		return err
	}

	if len(tables) == 0 {
		if jsonOut {
			shared.PrintJSON(map[string]any{"status": "ok", "dropped": []string{}, "message": "no tables to drop"})
			return nil
		}
		color.Yellow("No tables to drop.")
		return nil
	}

	if !jsonOut {
		fmt.Println()
		color.Set(color.Bold)
		fmt.Printf("%d table(s) will be dropped:\n", len(tables))
		color.Unset()
		for _, t := range tables {
			color.Cyan("  %s", t)
		}
		fmt.Println()

		if !r.AutoConfirm {
			if !shared.Confirm("This is destructive. Type 'yes' to proceed: ") {
				color.Yellow("Drop cancelled.")
				return shared.ErrCancelled
			}
		}
	}

	dropped, err := app.DropAllAction{DB: dbAdapter}.Execute(ctx)
	if err != nil {
		return fmt.Errorf("dropping tables: %w", err)
	}

	if jsonOut {
		shared.PrintJSON(map[string]any{"status": "ok", "dropped": dropped})
		return nil
	}

	color.Green("Dropped %d table(s).", len(dropped))
	return nil
}
