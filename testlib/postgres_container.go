package testlib

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

var (
	testPostgresDB  *sql.DB
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
