package infra

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// MySQLDBAdapter implements app.DBAdapter for MySQL.
type MySQLDBAdapter struct {
	db *sql.DB
}

// NewMySQLDBAdapter constructs the MySQL adapter.
func NewMySQLDBAdapter(db *sql.DB) *MySQLDBAdapter {
	return &MySQLDBAdapter{db: db}
}

// ListTables returns all tables in the current database, in alphabetical order.
func (m *MySQLDBAdapter) ListTables(ctx context.Context) ([]string, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = DATABASE()
		  AND table_type = 'BASE TABLE'
		ORDER BY table_name
	`)
	if err != nil {
		return nil, fmt.Errorf("listing tables: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scanning table name: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return names, nil
}

// DropAllTables drops every table in the current database. FK checks are
// disabled for the duration of the drop so circular constraints don't matter.
func (m *MySQLDBAdapter) DropAllTables(ctx context.Context) error {
	tables, err := m.ListTables(ctx)
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		return nil
	}

	quoted := make([]string, len(tables))
	for i, t := range tables {
		quoted[i] = "`" + t + "`"
	}

	stmt := "SET FOREIGN_KEY_CHECKS=0; DROP TABLE IF EXISTS " +
		strings.Join(quoted, ", ") + "; SET FOREIGN_KEY_CHECKS=1;"

	if _, err := m.db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("dropping tables: %w", err)
	}
	return nil
}
