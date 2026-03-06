package lock

import (
	"context"
	"database/sql"

	"github.com/fatih/color"
	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/cmd/shared"
	"github.com/apsdsm/joka/internal/domains/lock/app"
	lockinfra "github.com/apsdsm/joka/internal/domains/lock/infra"
)

// RunUnlockCommand handles the "unlock" command, which is an escape hatch to
// force-release a held lock. This is useful when a process crashes without
// cleaning up, leaving the lock row behind in joka_lock.
type RunUnlockCommand struct {
	DB           *sql.DB
	Driver       jokadb.Driver
	OutputFormat string
}

// Execute checks if a lock is currently held and releases it. If no lock is
// held, it prints a message and exits cleanly.
func (r RunUnlockCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON
	adapter := lockinfra.NewLockAdapter(r.Driver, r.DB)

	lock, err := adapter.GetLock(ctx)
	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		color.Red("Error checking lock: %v", err)
		return err
	}

	if lock == nil {
		if jsonOut {
			shared.PrintJSON(map[string]string{"status": "ok", "message": "no lock held"})
			return nil
		}
		color.Yellow("No lock is currently held.")
		return nil
	}

	if !jsonOut {
		color.Yellow("Releasing lock held by %s since %s (operation: %s)",
			lock.LockedBy, lock.LockedAt.Format("2006-01-02 15:04:05"), lock.Operation)
	}

	if err := (app.ReleaseLockAction{Lock: adapter}).Execute(ctx); err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		color.Red("Error releasing lock: %v", err)
		return err
	}

	if jsonOut {
		shared.PrintJSON(map[string]any{
			"status":    "ok",
			"message":   "lock released",
			"locked_by": lock.LockedBy,
			"locked_at": lock.LockedAt.Format("2006-01-02 15:04:05"),
			"operation": lock.Operation,
		})
		return nil
	}

	color.Green("Lock released.")
	return nil
}
