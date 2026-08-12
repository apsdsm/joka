package testlib

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	jokadb "github.com/apsdsm/joka/db"
)

var (
	testPostgresDB  *sql.DB
	testPostgresDSN string
	pgOnce          sync.Once
	pgInitErr       error
)

const (
	pgDBName     = "joka_test"
	pgDBUser     = "postgres"
	pgDBPassword = "test"
)

// GetTestPostgresDB returns a shared *sql.DB connected to the test PostgreSQL container.
// The container is started once per test run via sync.Once.
func GetTestPostgresDB() (*sql.DB, error) {
	pgOnce.Do(func() {
		testPostgresDB, pgInitErr = startPostgresContainer()
	})
	return testPostgresDB, pgInitErr
}

// startPostgresContainer starts a PostgreSQL 16 container and returns a connection to it.
func startPostgresContainer() (*sql.DB, error) {
	ctx := context.Background()

	container, err := postgres.Run(ctx,
		"postgres:16-alpine",
		postgres.WithDatabase(pgDBName),
		postgres.WithUsername(pgDBUser),
		postgres.WithPassword(pgDBPassword),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return nil, fmt.Errorf("starting postgres container: %w", err)
	}

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		container.Terminate(ctx) //nolint:errcheck
		return nil, fmt.Errorf("getting connection string: %w", err)
	}

	db, err := sql.Open("postgres", connStr)
	if err != nil {
		container.Terminate(ctx) //nolint:errcheck
		return nil, fmt.Errorf("opening database connection: %w", err)
	}

	db.SetConnMaxLifetime(time.Minute * 3)
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(10)

	if err := db.Ping(); err != nil {
		container.Terminate(ctx) //nolint:errcheck
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	testPostgresDSN = connStr
	return db, nil
}

// DropTablePostgres drops a table if it exists in PostgreSQL.
func DropTablePostgres(t *testing.T, db *sql.DB, tableName string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), fmt.Sprintf(`DROP TABLE IF EXISTS "%s"`, tableName))
	if err != nil {
		t.Logf("warning: failed to drop table %s: %v", tableName, err)
	}
}

// GetTestPostgresDSN returns the connection string for the test PostgreSQL
// container. Tests that shell out to pg_dump need the DSN, not just a *sql.DB.
// It starts the container if it is not already running.
func GetTestPostgresDSN() (string, error) {
	if _, err := GetTestPostgresDB(); err != nil {
		return "", err
	}
	return testPostgresDSN, nil
}

// ApplyToScratchPostgresDB creates a throwaway database, applies script to it
// using joka's own SQL splitter (the same path `migrate up` takes), and drops it
// again. It is how a generated baseline is proven to rebuild a schema from
// nothing. Any statement that fails is reported with the statement text.
func ApplyToScratchPostgresDB(t *testing.T, script, dbName string) error {
	t.Helper()
	ctx := context.Background()

	admin, err := GetTestPostgresDB()
	if err != nil {
		return fmt.Errorf("getting test db: %w", err)
	}
	dsn, err := GetTestPostgresDSN()
	if err != nil {
		return err
	}

	if _, err := admin.ExecContext(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %q`, dbName)); err != nil {
		return fmt.Errorf("dropping scratch database: %w", err)
	}
	if _, err := admin.ExecContext(ctx, fmt.Sprintf(`CREATE DATABASE %q`, dbName)); err != nil {
		return fmt.Errorf("creating scratch database: %w", err)
	}
	t.Cleanup(func() {
		if _, err := admin.ExecContext(context.Background(), fmt.Sprintf(`DROP DATABASE IF EXISTS %q`, dbName)); err != nil {
			t.Logf("warning: failed to drop scratch database %s: %v", dbName, err)
		}
	})

	scratchDSN, err := replacePostgresDBName(dsn, dbName)
	if err != nil {
		return err
	}
	scratch, err := sql.Open("postgres", scratchDSN)
	if err != nil {
		return fmt.Errorf("opening scratch database: %w", err)
	}
	defer scratch.Close()

	for _, stmt := range jokadb.SplitSQLStatements(script) {
		if _, err := scratch.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%w\n\nfailing statement:\n%s", err, strings.TrimSpace(stmt))
		}
	}
	return nil
}

// replacePostgresDBName swaps the database name in a postgres connection URL.
func replacePostgresDBName(dsn, dbName string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", fmt.Errorf("parsing test DSN: %w", err)
	}
	u.Path = "/" + dbName
	return u.String(), nil
}
