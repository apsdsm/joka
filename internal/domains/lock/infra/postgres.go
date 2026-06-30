package infra

import (
	"context"
	"database/sql"
	"fmt"

	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/domains/lock/domain"
)

// jokaLockKey is the fixed key for the PostgreSQL session-level advisory lock.
// The bytes spell "joka". A session-level advisory lock is held on a dedicated
// connection and is automatically released by PostgreSQL when that connection
// is closed — including when the process crashes — so a killed run can never
// leave the gate permanently held.
const jokaLockKey int64 = 0x6A6F6B61

// PostgresLockAdapter gates mutating operations behind a session-level advisory
// lock held on a dedicated connection. The joka_lock table is now purely
// informational ("who holds it, since when, for what") and is no longer the
// gate itself, so a stale row from a crashed run cannot block new runs.
type PostgresLockAdapter struct {
	conn   *sql.DB
	driver jokadb.Driver
	held   *sql.Conn
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

// Acquire takes a session-level advisory lock on a dedicated connection. If the
// lock is already held by a live session, it returns ErrLockHeld (enriched with
// the holder from the joka_lock row if one is present). On success it records
// the holder in the joka_lock visibility row, upserting so that a stale row left
// by a crashed run is overwritten rather than treated as a conflict.
func (p *PostgresLockAdapter) Acquire(ctx context.Context, operation string) error {
	if err := p.EnsureTable(ctx); err != nil {
		return fmt.Errorf("ensuring lock table: %w", err)
	}

	conn, err := p.conn.Conn(ctx)
	if err != nil {
		return fmt.Errorf("opening lock connection: %w", err)
	}

	var acquired bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, jokaLockKey).Scan(&acquired); err != nil {
		conn.Close() //nolint:errcheck
		return fmt.Errorf("acquiring advisory lock: %w", err)
	}

	if !acquired {
		conn.Close() //nolint:errcheck
		// Lock is held by a live session. Read the visibility row (if any) to
		// enrich the message with who holds it and what they're doing.
		lock, getErr := p.GetLock(ctx)
		if getErr != nil {
			return fmt.Errorf("%w (could not read holder: %v)", domain.ErrLockHeld, getErr)
		}
		if lock == nil {
			return domain.ErrLockHeld
		}
		return fmt.Errorf("%w: held by %s since %s (operation: %s)",
			domain.ErrLockHeld, lock.LockedBy, lock.LockedAt.Format("2006-01-02 15:04:05"), lock.Operation)
	}

	p.held = conn

	lockedBy := lockerIdentity()
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO joka_lock (id, locked_by, locked_at, operation)
		 VALUES (1, $1, NOW(), $2)
		 ON CONFLICT (id) DO UPDATE
		 SET locked_by = EXCLUDED.locked_by, locked_at = EXCLUDED.locked_at, operation = EXCLUDED.operation`,
		lockedBy, operation,
	); err != nil {
		// We hold the advisory lock; roll it back so we don't strand the conn.
		conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, jokaLockKey) //nolint:errcheck
		conn.Close()                                                        //nolint:errcheck
		p.held = nil
		return fmt.Errorf("recording lock holder: %w", err)
	}

	return nil
}

// Release frees the advisory lock and clears the visibility row. It is nil-safe:
// if this adapter never acquired the lock (held == nil), it just clears any
// leftover visibility row — this is the path `joka unlock` uses to tidy up a
// stale row from a crashed run (whose advisory lock is already gone).
func (p *PostgresLockAdapter) Release(ctx context.Context) error {
	if p.held == nil {
		if err := p.EnsureTable(ctx); err != nil {
			return fmt.Errorf("ensuring lock table: %w", err)
		}
		_, err := p.conn.ExecContext(ctx, `DELETE FROM joka_lock WHERE id = 1`)
		return err
	}

	conn := p.held
	p.held = nil
	defer conn.Close() //nolint:errcheck

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_unlock($1)`, jokaLockKey); err != nil {
		return fmt.Errorf("releasing advisory lock: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM joka_lock WHERE id = 1`); err != nil {
		return fmt.Errorf("clearing lock row: %w", err)
	}
	return nil
}

// GetLock reads the current lock holder from the visibility row. Returns nil if
// no row is present. Note the row is informational; the advisory lock is the
// real gate.
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
