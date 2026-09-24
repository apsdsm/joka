package lock

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/apsdsm/joka/cmd/shared"
	"github.com/apsdsm/joka/internal/domains/lock/app"
	lockinfra "github.com/apsdsm/joka/internal/domains/lock/infra"
	"github.com/fatih/color"
)

// RunUnlockCommand handles the "unlock" command, which is an escape hatch to
// force-release a held lock. This is useful when a process crashes without
// cleaning up, leaving the lock row behind in joka_lock.
type RunUnlockCommand struct {
	DB           *sql.DB
	OutputFormat string
}

// Execute checks if a lock is currently held and releases it. If no lock is
// held, it prints a message and exits cleanly.
func (r RunUnlockCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON
	adapter := lockinfra.NewPostgresLockAdapter(r.DB)

	lock, err := adapter.GetLock(ctx)
	if err != nil {
		return fmt.Errorf("checking the lock: %w", err)
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
		return fmt.Errorf("releasing the lock: %w", err)
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
