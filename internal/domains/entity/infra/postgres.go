package infra

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	jokadb "github.com/apsdsm/joka/db"
)

// PostgresDBAdapter implements entity app.DBAdapter for PostgreSQL.
type PostgresDBAdapter struct {
	db     DBTX
	conn   *sql.DB
	driver jokadb.Driver
}

// NewPostgresDBAdapter creates an adapter that runs all queries on the raw connection.
func NewPostgresDBAdapter(conn *sql.DB) *PostgresDBAdapter {
	return &PostgresDBAdapter{db: conn, conn: conn, driver: jokadb.Postgres}
}

// NewPostgresTxDBAdapter creates an adapter that runs InsertRow inside the
// given transaction, while tracking-table DDL uses the raw connection.
func NewPostgresTxDBAdapter(tx *sql.Tx, conn *sql.DB) *PostgresDBAdapter {
	return &PostgresDBAdapter{db: tx, conn: conn, driver: jokadb.Postgres}
}

// EnsureTrackingTable creates the joka_entities table if it does not already exist.
func (p *PostgresDBAdapter) EnsureTrackingTable(ctx context.Context) error {
	exists, err := jokadb.TableExists(ctx, p.conn, p.driver, "joka_entities")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	_, err = p.conn.ExecContext(ctx, `
		CREATE TABLE joka_entities (
			id BIGSERIAL PRIMARY KEY,
			entity_file VARCHAR(512) NOT NULL UNIQUE,
			synced_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	return err
}

// IsEntitySynced returns true if the given file path has already been recorded.
func (p *PostgresDBAdapter) IsEntitySynced(ctx context.Context, filePath string) (bool, error) {
	var exists int
	err := p.conn.QueryRowContext(ctx,
		`SELECT 1 FROM joka_entities WHERE entity_file = $1`,
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

// RecordEntitySynced inserts a row into joka_entities to mark the file as synced.
func (p *PostgresDBAdapter) RecordEntitySynced(ctx context.Context, filePath string) error {
	_, err := p.conn.ExecContext(ctx,
		`INSERT INTO joka_entities (entity_file) VALUES ($1)`,
		filePath,
	)
	return err
}

// InsertRow inserts a single row into the given table. For PostgreSQL, it uses
// RETURNING with the provided pkColumn to get the inserted row's auto-generated
// id (since LastInsertId is not supported by lib/pq).
func (p *PostgresDBAdapter) InsertRow(ctx context.Context, table string, columns map[string]any, pkColumn string) (int64, error) {
	colNames := make([]string, 0, len(columns))
	placeholders := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns))

	i := 1
	for k, v := range columns {
		colNames = append(colNames, fmt.Sprintf(`"%s"`, k))
		placeholders = append(placeholders, fmt.Sprintf("$%d", i))
		args = append(args, v)
		i++
	}

	query := fmt.Sprintf(`INSERT INTO "%s" (%s) VALUES (%s) RETURNING "%s"`,
		table,
		strings.Join(colNames, ", "),
		strings.Join(placeholders, ", "),
		pkColumn,
	)

	var id int64
	err := p.db.QueryRowContext(ctx, query, args...).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("inserting into %s: %w", table, err)
	}

	return id, nil
}
