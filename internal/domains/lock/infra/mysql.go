package infra

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"

	"github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/domains/lock/domain"
)

// MySQLLockAdapter provides advisory locking backed by the joka_lock table.
// The table holds at most one row (PRIMARY KEY = 1). An INSERT succeeding means
// the lock was acquired; a duplicate-key failure means it's held by someone else.
type MySQLLockAdapter struct {
	conn *sql.DB
}

// NewMySQLLockAdapter creates a lock adapter that operates on the given database connection.
func NewMySQLLockAdapter(conn *sql.DB) *MySQLLockAdapter {
	return &MySQLLockAdapter{conn: conn}
}

// EnsureTable creates the joka_lock table if it doesn't already exist.
// Called automatically by Acquire and Release so callers don't need to
// run `joka init` first.
func (m *MySQLLockAdapter) EnsureTable(ctx context.Context) error {
	exists, err := db.TableExists(ctx, m.conn, "joka_lock")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	_, err = m.conn.ExecContext(ctx, `
		CREATE TABLE joka_lock (
			id INT PRIMARY KEY DEFAULT 1,
			locked_by VARCHAR(255) NOT NULL,
			locked_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			operation VARCHAR(255) NOT NULL
		)
	`)
	return err
}

// Acquire attempts to take the lock by inserting a row with id=1.
// If the insert fails (duplicate key), the lock is already held and we return
// ErrLockHeld with details about who holds it. The operation string is stored
// so that other users can see what command is running.
func (m *MySQLLockAdapter) Acquire(ctx context.Context, operation string) error {
	if err := m.EnsureTable(ctx); err != nil {
		return fmt.Errorf("ensuring lock table: %w", err)
	}

	lockedBy := lockerIdentity()

	_, err := m.conn.ExecContext(ctx,
		`INSERT INTO joka_lock (id, locked_by, operation) VALUES (1, ?, ?)`,
		lockedBy, operation,
	)
	if err != nil {
		// Insert failed — lock is held. Read who holds it.
		lock, getErr := m.GetLock(ctx)
		if getErr != nil {
			return fmt.Errorf("%w (could not read holder: %v)", domain.ErrLockHeld, getErr)
		}
		return fmt.Errorf("%w: held by %s since %s (operation: %s)",
			domain.ErrLockHeld, lock.LockedBy, lock.LockedAt.Format("2006-01-02 15:04:05"), lock.Operation)
	}
	return nil
}

// Release deletes the lock row, allowing other processes to acquire it.
// Safe to call even if no lock is held (DELETE WHERE id=1 on an empty table is a no-op).
func (m *MySQLLockAdapter) Release(ctx context.Context) error {
	if err := m.EnsureTable(ctx); err != nil {
		return fmt.Errorf("ensuring lock table: %w", err)
	}

	_, err := m.conn.ExecContext(ctx, `DELETE FROM joka_lock WHERE id = 1`)
	return err
}

// GetLock reads the current lock holder. Returns nil if no lock is held.
func (m *MySQLLockAdapter) GetLock(ctx context.Context) (*domain.Lock, error) {
	var lock domain.Lock
	err := m.conn.QueryRowContext(ctx,
		`SELECT id, locked_by, locked_at, operation FROM joka_lock WHERE id = 1`,
	).Scan(&lock.ID, &lock.LockedBy, &lock.LockedAt, &lock.Operation)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &lock, nil
}

// lockerIdentity returns a "hostname:pid" string that identifies the current process.
// Used as the locked_by value so operators can tell which machine/process holds the lock.
func lockerIdentity() string {
	hostname, _ := os.Hostname()
	return hostname + ":" + strconv.Itoa(os.Getpid())
}
