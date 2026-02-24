package infra

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/apsdsm/joka/db"
)

// DBTX is satisfied by both *sql.DB and *sql.Tx, allowing the adapter to
// operate inside or outside a transaction.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// MySQLDBAdapter implements entity app.DBAdapter for MySQL. Tracking-table
// operations use the raw *sql.DB connection (DDL can't run in a transaction),
// while InsertRow uses the DBTX interface (typically a *sql.Tx).
type MySQLDBAdapter struct {
	db   DBTX
	conn *sql.DB
}

// NewMySQLDBAdapter creates an adapter that runs all queries on the raw
// connection (outside a transaction).
func NewMySQLDBAdapter(conn *sql.DB) *MySQLDBAdapter {
	return &MySQLDBAdapter{db: conn, conn: conn}
}

// NewMySQLTxDBAdapter creates an adapter that runs InsertRow inside the
// given transaction, while tracking-table DDL uses the raw connection.
func NewMySQLTxDBAdapter(tx *sql.Tx, conn *sql.DB) *MySQLDBAdapter {
	return &MySQLDBAdapter{db: tx, conn: conn}
}

// EnsureTrackingTable creates the joka_entities table if it does not already
// exist. The table tracks which entity files have been synced.
func (m *MySQLDBAdapter) EnsureTrackingTable(ctx context.Context) error {
	exists, err := db.TableExists(ctx, m.conn, "joka_entities")
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
// (transaction or raw connection). Returns the last insert id.
func (m *MySQLDBAdapter) InsertRow(ctx context.Context, table string, columns map[string]any) (int64, error) {
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
