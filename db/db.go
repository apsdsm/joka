package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

// ErrUnsupportedDriver is returned for a DSN that is not PostgreSQL. joka
// supported MySQL until v0.14.0; the drivers had diverged far enough that
// keeping both honest cost more than it was worth, and consolidation was
// PostgreSQL-only anyway. The check exists so a MySQL DSN gets this sentence
// rather than a connection error from the PostgreSQL driver.
var ErrUnsupportedDriver = errors.New("joka supports PostgreSQL only")

// IsPostgresDSN reports whether a DSN names a PostgreSQL database.
func IsPostgresDSN(dsn string) bool {
	lower := strings.ToLower(strings.TrimSpace(dsn))
	return strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://")
}

// Open creates and verifies a PostgreSQL connection from a DSN string. The
// caller is responsible for closing the returned *sql.DB.
func Open(dsn string) (*sql.DB, error) {
	if !IsPostgresDSN(dsn) {
		return nil, fmt.Errorf("%w: the connection URL must start with postgres:// or postgresql://", ErrUnsupportedDriver)
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// TableExists checks whether a table with the given name exists in the current
// schema.
func TableExists(ctx context.Context, db *sql.DB, tableName string) (bool, error) {
	const query = `
		SELECT 1
		FROM information_schema.tables
		WHERE table_name = $1
		AND table_schema = current_schema()
	`

	var exists int
	err := db.QueryRowContext(ctx, query, tableName).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking table existence: %w", err)
	}
	return exists == 1, nil
}

// OpenWait is Open, retrying the ping until it succeeds or the deadline
// passes.
//
// It exists for the container case. A compose stack starts joka beside the
// database it is meant to migrate, and Open pings once and fails, so every
// entrypoint script in every consuming project grew its own readiness loop —
// each one reimplementing the same wait against a different tool (pg_isready,
// nc, a psql call) with a different idea of how long to allow.
//
// A zero or negative wait is Open, so the caller does not special-case it.
//
// The retry is on the ping rather than on sql.Open, which does not connect. A
// DSN joka refuses is refused immediately: waiting out a timeout for a URL
// that can never work is the opposite of helpful.
func OpenWait(ctx context.Context, dsn string, wait time.Duration) (*sql.DB, error) {
	if wait <= 0 {
		return Open(dsn)
	}

	if !IsPostgresDSN(dsn) {
		return nil, fmt.Errorf("%w: the connection URL must start with postgres:// or postgresql://", ErrUnsupportedDriver)
	}

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}

	deadline := time.Now().Add(wait)
	var lastErr error

	for {
		if lastErr = db.PingContext(ctx); lastErr == nil {
			return db, nil
		}

		// The context going away is the caller giving up, which is not the
		// same as the database being slow, and must not be retried.
		if ctx.Err() != nil {
			db.Close()
			return nil, ctx.Err()
		}

		if !time.Now().Add(pingInterval).Before(deadline) {
			db.Close()
			return nil, fmt.Errorf("database not reachable after %s: %w", wait, lastErr)
		}

		select {
		case <-ctx.Done():
			db.Close()
			return nil, ctx.Err()
		case <-time.After(pingInterval):
		}
	}
}

// pingInterval is how often OpenWait retries. Half a second is short enough
// that a database that comes up quickly is not kept waiting and long enough
// not to spin.
const pingInterval = 500 * time.Millisecond
