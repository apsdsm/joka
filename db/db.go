package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"
	_ "github.com/lib/pq"
)

// Driver identifies which database backend is in use.
type Driver int

const (
	MySQL    Driver = iota
	Postgres
)

// String returns the driver name for display purposes.
func (d Driver) String() string {
	switch d {
	case Postgres:
		return "postgres"
	default:
		return "mysql"
	}
}

// DetectDriver examines a DSN string and returns the appropriate driver.
// PostgreSQL DSNs start with "postgres://" or "postgresql://".
// Everything else is assumed to be MySQL.
func DetectDriver(dsn string) Driver {
	lower := strings.ToLower(dsn)
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		return Postgres
	}
	return MySQL
}

// ensureMySQLFlags parses a MySQL DSN and enables flags that joka requires:
//   - MultiStatements: allows migration files with multiple SQL statements.
//   - ParseTime: scans DATETIME/TIMESTAMP columns into time.Time values.
func ensureMySQLFlags(dsn string) (string, error) {
	cfg, err := mysql.ParseDSN(dsn)
	if err != nil {
		return "", fmt.Errorf("parsing DSN: %w", err)
	}
	cfg.MultiStatements = true
	cfg.ParseTime = true
	return cfg.FormatDSN(), nil
}

// Open creates and verifies a database connection from a DSN string.
// It auto-detects the driver from the DSN format. For MySQL, it enables
// multiStatements and parseTime. The caller is responsible for closing the
// returned *sql.DB.
func Open(dsn string) (*sql.DB, Driver, error) {
	driver := DetectDriver(dsn)

	switch driver {
	case Postgres:
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			return nil, driver, err
		}
		if err := db.Ping(); err != nil {
			db.Close()
			return nil, driver, err
		}
		return db, driver, nil

	default:
		dsn, err := ensureMySQLFlags(dsn)
		if err != nil {
			return nil, driver, err
		}
		db, err := sql.Open("mysql", dsn)
		if err != nil {
			return nil, driver, err
		}
		if err := db.Ping(); err != nil {
			db.Close()
			return nil, driver, err
		}
		return db, driver, nil
	}
}

// TableExists checks whether a table with the given name exists in the
// current database schema.
func TableExists(ctx context.Context, db *sql.DB, driver Driver, tableName string) (bool, error) {
	var query string
	switch driver {
	case Postgres:
		query = `
			SELECT 1
			FROM information_schema.tables
			WHERE table_name = $1
			AND table_schema = current_schema()
		`
	default:
		query = `
			SELECT 1
			FROM information_schema.tables
			WHERE table_name = ?
			AND table_schema = DATABASE()
		`
	}

	var exists int
	err := db.QueryRowContext(ctx, query, tableName).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking table existence: %w", err)
	}
	return exists == 1, nil
}
