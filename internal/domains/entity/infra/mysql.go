package infra

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// DBTX is satisfied by both *sql.DB and *sql.Tx, allowing the adapter to
// operate inside or outside a transaction.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// MySQLDBAdapter implements entity app.DBAdapter for MySQL. Tracking-table
// operations use the raw *sql.DB connection (DDL can't run in a transaction),
// while InsertRow uses the DBTX interface (typically a *sql.Tx).
type MySQLDBAdapter struct {
	db     DBTX
	conn   *sql.DB
	driver jokadb.Driver
}

// NewMySQLDBAdapter creates an adapter that runs all queries on the raw
// connection (outside a transaction).
func NewMySQLDBAdapter(conn *sql.DB) *MySQLDBAdapter {
	return &MySQLDBAdapter{db: conn, conn: conn, driver: jokadb.MySQL}
}

// NewMySQLTxDBAdapter creates an adapter that runs InsertRow inside the
// given transaction, while tracking-table DDL uses the raw connection.
func NewMySQLTxDBAdapter(tx *sql.Tx, conn *sql.DB) *MySQLDBAdapter {
	return &MySQLDBAdapter{db: tx, conn: conn, driver: jokadb.MySQL}
}

// EnsureTrackingTable creates the joka_entities table if it does not already
// exist. The table tracks which entity files have been synced.
func (m *MySQLDBAdapter) EnsureTrackingTable(ctx context.Context) error {
	exists, err := jokadb.TableExists(ctx, m.conn, m.driver, "joka_entities")
	if err != nil {
		return err
	}

	if exists {
		return nil
	}

	_, err = m.conn.ExecContext(ctx, `
		CREATE TABLE joka_entities (
			id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
			entity_file VARCHAR(512) NOT NULL UNIQUE,
			synced_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)

	return err
}

// IsEntitySynced returns true if the given file path has already been recorded
// in the joka_entities table.
func (m *MySQLDBAdapter) IsEntitySynced(ctx context.Context, filePath string) (bool, error) {
	var exists int

	err := m.conn.QueryRowContext(ctx,
		`SELECT 1 FROM joka_entities WHERE entity_file = ?`,
		filePath,
	).Scan(&exists)

	if err == sql.ErrNoRows {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("checking entity sync status: %w", err)
	}

	return true, nil
}

// RecordEntitySynced inserts a row into joka_entities to mark the file as
// synced. Uses the raw connection so it persists even if the insert transaction
// is rolled back.
func (m *MySQLDBAdapter) RecordEntitySynced(ctx context.Context, filePath string) error {
	_, err := m.conn.ExecContext(ctx,
		`INSERT INTO joka_entities (entity_file) VALUES (?)`,
		filePath,
	)

	return err
}

// InsertRow inserts a single row into the given table using the DBTX interface
// (transaction or raw connection). Returns the last insert id. The pkColumn
// parameter is accepted for interface compatibility but MySQL uses LastInsertId.
func (m *MySQLDBAdapter) InsertRow(ctx context.Context, table string, columns map[string]any, pkColumn string) (int64, error) {
	colNames := make([]string, 0, len(columns))
	placeholders := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))

	for k, v := range columns {
		colNames = append(colNames, fmt.Sprintf("`%s`", k))
		placeholders = append(placeholders, "?")
		args = append(args, v)
	}

	query := fmt.Sprintf("INSERT INTO `%s` (%s) VALUES (%s)",
		table,
		strings.Join(colNames, ", "),
		strings.Join(placeholders, ", "),
	)

	result, err := m.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("inserting into %s: %w", table, err)
	}

	return result.LastInsertId()
}

// LookupValue queries a single value from an existing table row. Returns
// ErrLookupNotFound if no matching row exists.
func (m *MySQLDBAdapter) LookupValue(ctx context.Context, table, returnCol, whereCol string, whereVal any) (any, error) {
	query := fmt.Sprintf("SELECT `%s` FROM `%s` WHERE `%s` = ? LIMIT 1", returnCol, table, whereCol)

	var result any

	err := m.db.QueryRowContext(ctx, query, whereVal).Scan(&result)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: %s.%s where %s=%v", domain.ErrLookupNotFound, table, returnCol, whereCol, whereVal)
	}

	if err != nil {
		return nil, fmt.Errorf("lookup %s.%s: %w", table, returnCol, err)
	}

	// MySQL driver returns []byte for string columns; convert for usability.
	if b, ok := result.([]byte); ok {
		return string(b), nil
	}

	return result, nil
}
