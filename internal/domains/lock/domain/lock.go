package domain

import (
	"errors"
	"time"
)

// ErrLockHeld is returned when a mutating command (e.g. migrate up, data sync)
// tries to acquire the joka_lock but another process already holds it.
var ErrLockHeld = errors.New("lock is already held")

// Lock represents a row in the joka_lock table. At most one row can exist
// (id is always 1), so the presence of a row means a lock is held.
type Lock struct {
	ID        int
	LockedBy  string    // hostname:pid of the process that acquired the lock
	LockedAt  time.Time // when the lock was acquired
	Operation string    // which command holds the lock (e.g. "migrate up")
}
