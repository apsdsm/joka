package entity

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/apsdsm/joka/cmd/shared"
	"github.com/apsdsm/joka/internal/domains/entity/app"
	"github.com/apsdsm/joka/internal/domains/entity/infra"
	lockinfra "github.com/apsdsm/joka/internal/domains/lock/infra"
	"github.com/fatih/color"
)

// RunEntityReimportCommand handles the "entity reimport" command.
type RunEntityReimportCommand struct {
	DB *sql.DB
	// Secrets resolves {{ asm.<source>.<key> }} template references against the
	// `secrets:` sources in .jokarc.yaml.
	Secrets      app.SecretResolver
	EntitiesDir  string
	FilePath     string // relative path argument
	AutoConfirm  bool
	OutputFormat string
	// Prune deletes rows the file no longer declares. Without it they are
	// left in the database and reported.
	Prune bool
	// StateFile overrides where the state file is written; empty means the
	// default beside the working directory.
	StateFile string
	// Profile and JokaVersion name the state file and stamp it.
	Profile     string
	JokaVersion string
}

func (r RunEntityReimportCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON

	lockAdapter := lockinfra.NewPostgresLockAdapter(r.DB)

	if err := lockAdapter.Acquire(ctx, "entity reimport"); err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}
	defer lockAdapter.Release(ctx) //nolint:errcheck

	if err := infra.NewPostgresStateBackend(r.DB).EnsureStateTable(ctx); err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	fullPath := filepath.Join(r.EntitiesDir, r.FilePath)

	// The whole set is read and validated, not just the named file. An _id is
	// unique across the set, and an entity can be tracked against a file other
	// than the one declaring it — reading one file cannot see either.
	set, err := loadSet(r.EntitiesDir)
	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	if _, err := declaredIn(set, r.FilePath); err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	// Read for the preview only. The action loads its own copy inside the
	// transaction, which is what it writes back.
	state, err := infra.NewPostgresStateBackend(r.DB).Load(ctx)
	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	if _, synced := state.FileHash(r.FilePath); !synced {
		err = fmt.Errorf("entity file %q has never been synced; use 'entity sync' first", r.FilePath)
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	tracked := state.RowsInFile(r.FilePath)

	contentHash, err := app.HashFileContent(fullPath)
	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	if !jsonOut {
		fmt.Println()
		color.Set(color.Bold)
		fmt.Println("Entity reimport:")
		color.Unset()
		color.Cyan("  File: %s", r.FilePath)
		color.Cyan("  Tracked rows: %d", len(tracked))
		if r.Prune {
			color.Yellow("  --prune: rows the file no longer declares will be deleted too")
		} else {
			fmt.Println("  Rows the file no longer declares are left alone (--prune deletes them)")
		}
		fmt.Println()

		if !r.AutoConfirm {
			if !shared.Confirm("Proceed with entity reimport? (only 'yes' will confirm): ") {
				color.Yellow("Entity reimport cancelled.")
				return nil
			}
		}
	}

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(fmt.Errorf("starting transaction: %w", err))
		}
		return fmt.Errorf("starting transaction: %w", err)
	}

	txAdapter := infra.NewPostgresTxDBAdapter(tx, r.DB)

	undeclared, err := app.ReimportEntityAction{
		DB:          txAdapter,
		Backend:     infra.NewPostgresTxStateBackend(tx, r.DB),
		Secrets:     r.Secrets,
		FilePath:    r.FilePath,
		FullPath:    fullPath,
		ContentHash: contentHash,
		Prune:       r.Prune,
	}.Execute(ctx)
	if err != nil {
		tx.Rollback() //nolint:errcheck
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	if err := tx.Commit(); err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(fmt.Errorf("committing transaction: %w", err))
		}
		return fmt.Errorf("committing transaction: %w", err)
	}

	if err := materializeState(ctx, r.DB, r.StateFile, r.Profile, r.JokaVersion); err != nil {
		color.Yellow("The change committed, but the state file was not written: %v", err)
	}

	if jsonOut {
		shared.PrintJSON(map[string]any{
			"status": "ok", "file": r.FilePath, "rows": len(tracked),
			"undeclared": undeclaredJSON(undeclared),
		})
		return nil
	}

	color.Green("\nEntity reimport complete: %s", r.FilePath)
	reportUndeclared(undeclared)
	return nil
}
