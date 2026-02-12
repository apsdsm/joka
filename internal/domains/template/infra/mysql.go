package infra

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/domains/template/domain"
)

type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

type MySQLDBAdapter struct {
	db   DBTX
	conn *sql.DB
}

func NewMySQLDBAdapter(conn *sql.DB) *MySQLDBAdapter {
	return &MySQLDBAdapter{db: conn, conn: conn}
}

func NewMySQLTxDBAdapter(tx *sql.Tx, conn *sql.DB) *MySQLDBAdapter {
	return &MySQLDBAdapter{db: tx, conn: conn}
}

func (m *MySQLDBAdapter) TruncateTable(ctx context.Context, tableName string) error {
	exists, err := db.TableExists(ctx, m.conn, tableName)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: %s", domain.ErrTableNotFound, tableName)
	}

	_, err = m.db.ExecContext(ctx, fmt.Sprintf("TRUNCATE TABLE `%s`", tableName))
	return err
}

func (m *MySQLDBAdapter) InsertRows(ctx context.Context, tableName string, rows []map[string]any) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	exists, err := db.TableExists(ctx, m.conn, tableName)
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, fmt.Errorf("%w: %s", domain.ErrTableNotFound, tableName)
	}

	var columns []string
	for k := range rows[0] {
		columns = append(columns, k)
	}

	colNames := make([]string, len(columns))
	placeholders := make([]string, len(columns))
	for i, c := range columns {
		colNames[i] = fmt.Sprintf("`%s`", c)
		placeholders[i] = "?"
	}

	query := fmt.Sprintf("INSERT INTO `%s` (%s) VALUES (%s)",
		tableName,
		strings.Join(colNames, ", "),
		strings.Join(placeholders, ", "),
	)

	for _, row := range rows {
		args := make([]any, len(columns))
		for i, c := range columns {
			args[i] = row[c]
		}
		if _, err := m.db.ExecContext(ctx, query, args...); err != nil {
			return 0, err
		}
	}

	return len(rows), nil
}
