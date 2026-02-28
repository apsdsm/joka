package app

import (
	"context"

	"github.com/apsdsm/joka/internal/domains/lock/domain"
)

// LockAdapter abstracts the lock storage so commands don't depend on a specific database.
type LockAdapter interface {
	Acquire(ctx context.Context, operation string) error
	Release(ctx context.Context) error
	GetLock(ctx context.Context) (*domain.Lock, error)
}

// AcquireLockAction attempts to take the advisory lock before a mutating
// operation (e.g. migrate up, data sync). If another process holds the lock,
// Execute returns an error with details about the holder.
type AcquireLockAction struct {
	Lock      LockAdapter
	Operation string
}

// Execute acquires the lock for the configured operation.
func (a AcquireLockAction) Execute(ctx context.Context) error {
	return a.Lock.Acquire(ctx, a.Operation)
}

// ReleaseLockAction releases the advisory lock after a mutating operation completes.
type ReleaseLockAction struct {
	Lock LockAdapter
}

// Execute releases the lock.
func (a ReleaseLockAction) Execute(ctx context.Context) error {
	return a.Lock.Release(ctx)
}
