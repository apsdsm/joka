package lock

import (
	"context"
	"database/sql"

	"github.com/fatih/color"
	"github.com/apsdsm/joka/internal/domains/lock/app"
	"github.com/apsdsm/joka/internal/domains/lock/infra"
)

// RunUnlockCommand handles the "unlock" command, which is an escape hatch to
// force-release a held lock. This is useful when a process crashes without
// cleaning up, leaving the lock row behind in joka_lock.
type RunUnlockCommand struct {
	DB *sql.DB
}

// Execute checks if a lock is currently held and releases it. If no lock is
// held, it prints a message and exits cleanly.
func (r RunUnlockCommand) Execute(ctx context.Context) error {
	adapter := infra.NewMySQLLockAdapter(r.DB)

	lock, err := adapter.GetLock(ctx)
	if err != nil {
		color.Red("Error checking lock: %v", err)
		return err
	}

	if lock == nil {
		color.Yellow("No lock is currently held.")
		return nil
	}

	color.Yellow("Releasing lock held by %s since %s (operation: %s)",
		lock.LockedBy, lock.LockedAt.Format("2006-01-02 15:04:05"), lock.Operation)

	if err := (app.ReleaseLockAction{Lock: adapter}).Execute(ctx); err != nil {
		color.Red("Error releasing lock: %v", err)
		return err
	}

	color.Green("Lock released.")
	return nil
}
