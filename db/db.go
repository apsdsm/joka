package db

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/go-sql-driver/mysql"
)

// EnsureMultiStatements parses a MySQL DSN, enables multiStatements, and
// returns the updated DSN string. Without this flag, go-sql-driver silently
// ignores everything after the first semicolon in a query — which means
// migration files with multiple statements would only partially execute.
func EnsureMultiStatements(dsn string) (string, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return "", fmt.Errorf("parsing DSN: %w", err)
	}
	cfg.MultiStatements = true
	return cfg.FormatDSN(), nil
}

// Open creates and verifies a database connection from a MySQL DSN string.
// It enables multiStatements and pings the database to ensure connectivity
// before returning. The caller is responsible for closing the returned *sql.DB.
func Open(dsn string) (*sql.DB, error) {
	dsn, err := EnsureMultiStatements(dsn)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// TableExists checks whether a table with the given name exists in the
// current database schema.
func TableExists(ctx context.Context, db *sql.DB, tableName string) (bool, error) {
	var exists int
	err := db.QueryRowContext(ctx, `
		SELECT 1
		FROM information_schema.tables
		WHERE table_name = ?
		AND table_schema = DATABASE()
	`, tableName).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking table existence: %w", err)
	}
	return exists == 1, nil
}
