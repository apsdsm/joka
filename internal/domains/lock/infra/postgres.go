package infra

import (
	"context"
	"database/sql"
	"fmt"

	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/domains/lock/domain"
)

// PostgresLockAdapter provides advisory locking backed by the joka_lock table
// for PostgreSQL databases.
type PostgresLockAdapter struct {
	conn   *sql.DB
	driver jokadb.Driver
}

// NewPostgresLockAdapter creates a lock adapter for PostgreSQL.
func NewPostgresLockAdapter(conn *sql.DB) *PostgresLockAdapter {
	return &PostgresLockAdapter{conn: conn, driver: jokadb.Postgres}
}

// EnsureTable creates the joka_lock table if it doesn't already exist.
func (p *PostgresLockAdapter) EnsureTable(ctx context.Context) error {
	exists, err := jokadb.TableExists(ctx, p.conn, p.driver, "joka_lock")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	_, err = p.conn.ExecContext(ctx, `
		CREATE TABLE joka_lock (
			id INTEGER PRIMARY KEY DEFAULT 1,
			locked_by VARCHAR(255) NOT NULL,
			locked_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			operation VARCHAR(255) NOT NULL
		)
	`)
	return err
}

// Acquire attempts to take the lock by inserting a row with id=1.
func (p *PostgresLockAdapter) Acquire(ctx context.Context, operation string) error {
	if err := p.EnsureTable(ctx); err != nil {
		return fmt.Errorf("ensuring lock table: %w", err)
	}

	lockedBy := lockerIdentity()

	_, err := p.conn.ExecContext(ctx,
		`INSERT INTO joka_lock (id, locked_by, operation) VALUES (1, $1, $2)`,
		lockedBy, operation,
	)
	if err != nil {
		lock, getErr := p.GetLock(ctx)
		if getErr != nil {
			return fmt.Errorf("%w (could not read holder: %v)", domain.ErrLockHeld, getErr)
		}
		return fmt.Errorf("%w: held by %s since %s (operation: %s)",
			domain.ErrLockHeld, lock.LockedBy, lock.LockedAt.Format("2006-01-02 15:04:05"), lock.Operation)
	}
	return nil
}

// Release deletes the lock row.
func (p *PostgresLockAdapter) Release(ctx context.Context) error {
	if err := p.EnsureTable(ctx); err != nil {
		return fmt.Errorf("ensuring lock table: %w", err)
	}

	_, err := p.conn.ExecContext(ctx, `DELETE FROM joka_lock WHERE id = 1`)
	return err
}

// GetLock reads the current lock holder. Returns nil if no lock is held.
func (p *PostgresLockAdapter) GetLock(ctx context.Context) (*domain.Lock, error) {
	if err := p.EnsureTable(ctx); err != nil {
		return nil, fmt.Errorf("ensuring lock table: %w", err)
	}

	var lock domain.Lock
	err := p.conn.QueryRowContext(ctx,
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
