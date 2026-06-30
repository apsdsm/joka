package infra

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strconv"

	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/domains/lock/domain"
)

// jokaLockName is the named MySQL advisory lock used to gate mutating
// operations. GET_LOCK is session-scoped: the lock is held on a dedicated
// connection and is automatically released when that connection is closed —
// including on process crash — so a killed run can never leave the gate held.
const jokaLockName = "joka_migrate"

// MySQLLockAdapter gates mutating operations behind a session-level named
// advisory lock (GET_LOCK) held on a dedicated connection. The joka_lock table
// is now purely informational and is no longer the gate, so a stale row left by
// a crashed run cannot block new runs.
type MySQLLockAdapter struct {
	conn   *sql.DB
	driver jokadb.Driver
	held   *sql.Conn
}

// NewMySQLLockAdapter creates a lock adapter that operates on the given database connection.
func NewMySQLLockAdapter(conn *sql.DB) *MySQLLockAdapter {
	return &MySQLLockAdapter{conn: conn, driver: jokadb.MySQL}
}

// EnsureTable creates the joka_lock table if it doesn't already exist.
// Called automatically by Acquire and Release so callers don't need to
// run `joka init` first.
func (m *MySQLLockAdapter) EnsureTable(ctx context.Context) error {
	exists, err := jokadb.TableExists(ctx, m.conn, m.driver, "joka_lock")
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

// Acquire takes a named session-level advisory lock on a dedicated connection.
// If the lock is already held by a live session it returns ErrLockHeld
// (enriched with the holder from the joka_lock row if present). On success it
// records the holder in the joka_lock visibility row, upserting so a stale row
// left by a crashed run is overwritten rather than treated as a conflict.
func (m *MySQLLockAdapter) Acquire(ctx context.Context, operation string) error {
	if err := m.EnsureTable(ctx); err != nil {
		return fmt.Errorf("ensuring lock table: %w", err)
	}

	conn, err := m.conn.Conn(ctx)
	if err != nil {
		return fmt.Errorf("opening lock connection: %w", err)
	}

	// GET_LOCK returns 1 if acquired, 0 if the timeout (0s) elapsed while held
	// by another session, NULL on error. Use a nullable int to detect NULL.
	var result sql.NullInt64
	if err := conn.QueryRowContext(ctx, `SELECT GET_LOCK(?, 0)`, jokaLockName).Scan(&result); err != nil {
		conn.Close() //nolint:errcheck
		return fmt.Errorf("acquiring advisory lock: %w", err)
	}

	if !result.Valid || result.Int64 != 1 {
		conn.Close() //nolint:errcheck
		lock, getErr := m.GetLock(ctx)
		if getErr != nil {
			return fmt.Errorf("%w (could not read holder: %v)", domain.ErrLockHeld, getErr)
		}
		if lock == nil {
			return domain.ErrLockHeld
		}
		return fmt.Errorf("%w: held by %s since %s (operation: %s)",
			domain.ErrLockHeld, lock.LockedBy, lock.LockedAt.Format("2006-01-02 15:04:05"), lock.Operation)
	}

	m.held = conn

	lockedBy := lockerIdentity()
	if _, err := conn.ExecContext(ctx,
		`INSERT INTO joka_lock (id, locked_by, locked_at, operation)
		 VALUES (1, ?, CURRENT_TIMESTAMP, ?)
		 ON DUPLICATE KEY UPDATE locked_by = VALUES(locked_by), locked_at = VALUES(locked_at), operation = VALUES(operation)`,
		lockedBy, operation,
	); err != nil {
		conn.ExecContext(ctx, `SELECT RELEASE_LOCK(?)`, jokaLockName) //nolint:errcheck
		conn.Close()                                                  //nolint:errcheck
		m.held = nil
		return fmt.Errorf("recording lock holder: %w", err)
	}

	return nil
}

// Release frees the advisory lock and clears the visibility row. It is nil-safe:
// if this adapter never acquired the lock (held == nil), it just clears any
// leftover visibility row — this is the path `joka unlock` uses to tidy up a
// stale row from a crashed run (whose advisory lock is already gone).
func (m *MySQLLockAdapter) Release(ctx context.Context) error {
	if m.held == nil {
		if err := m.EnsureTable(ctx); err != nil {
			return fmt.Errorf("ensuring lock table: %w", err)
		}
		_, err := m.conn.ExecContext(ctx, `DELETE FROM joka_lock WHERE id = 1`)
		return err
	}

	conn := m.held
	m.held = nil
	defer conn.Close() //nolint:errcheck

	if _, err := conn.ExecContext(ctx, `SELECT RELEASE_LOCK(?)`, jokaLockName); err != nil {
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
func (m *MySQLLockAdapter) GetLock(ctx context.Context) (*domain.Lock, error) {
	if err := m.EnsureTable(ctx); err != nil {
		return nil, fmt.Errorf("ensuring lock table: %w", err)
	}

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
