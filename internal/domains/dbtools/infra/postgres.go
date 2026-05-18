package infra

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// PostgresDBAdapter implements app.DBAdapter for PostgreSQL.
type PostgresDBAdapter struct {
	db *sql.DB
}

// NewPostgresDBAdapter constructs the Postgres adapter.
func NewPostgresDBAdapter(db *sql.DB) *PostgresDBAdapter {
	return &PostgresDBAdapter{db: db}
}

// ListTables returns all tables in the current schema, in alphabetical order.
func (p *PostgresDBAdapter) ListTables(ctx context.Context) ([]string, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT tablename
		FROM pg_tables
		WHERE schemaname = current_schema()
		ORDER BY tablename
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

// DropAllTables drops every table in the current schema with CASCADE so that
// foreign key dependencies between tables are resolved automatically.
func (p *PostgresDBAdapter) DropAllTables(ctx context.Context) error {
	tables, err := p.ListTables(ctx)
	if err != nil {
		return err
	}
	if len(tables) == 0 {
		return nil
	}

	quoted := make([]string, len(tables))
	for i, t := range tables {
		quoted[i] = `"` + t + `"`
	}

	stmt := "DROP TABLE IF EXISTS " + strings.Join(quoted, ", ") + " CASCADE"
	if _, err := p.db.ExecContext(ctx, stmt); err != nil {
		return fmt.Errorf("dropping tables: %w", err)
	}
	return nil
}
