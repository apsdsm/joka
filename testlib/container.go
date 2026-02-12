package testlib

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/testcontainers/testcontainers-go/modules/mysql"
)

var (
	testDB  *sql.DB
	once    sync.Once
	initErr error
)

const (
	dbName     = "joka_test"
	dbUser     = "root"
	dbPassword = "test"
)

// GetTestDB returns a shared *sql.DB connected to the test MySQL container.
// The container is started once per test run via sync.Once.
func GetTestDB() (*sql.DB, error) {
	once.Do(func() {
		testDB, initErr = startContainer()
	})
	return testDB, initErr
}

// startContainer starts a MySQL 8 container and returns a connection to it.
func startContainer() (*sql.DB, error) {
	ctx := context.Background()

	container, err := mysql.Run(ctx,
		"mysql:8",
		mysql.WithDatabase(dbName),
		mysql.WithUsername(dbUser),
		mysql.WithPassword(dbPassword),
	)
	if err != nil {
		return nil, fmt.Errorf("starting mysql container: %w", err)
	}

	connStr, err := container.ConnectionString(ctx, "parseTime=true", "charset=utf8mb4", "multiStatements=true")
	if err != nil {
		container.Terminate(ctx) //nolint:errcheck
		return nil, fmt.Errorf("getting connection string: %w", err)
	}

	db, err := sql.Open("mysql", connStr)
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

// DropTable drops a table if it exists. Useful in t.Cleanup to remove tables
// created by DDL statements (which auto-commit and can't be rolled back).
func DropTable(t *testing.T, db *sql.DB, tableName string) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), fmt.Sprintf("DROP TABLE IF EXISTS `%s`", tableName))
	if err != nil {
		t.Logf("warning: failed to drop table %s: %v", tableName, err)
	}
}
