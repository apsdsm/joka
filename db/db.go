package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

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
