package infra

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/domains/template/domain"
)

type PostgresDBAdapter struct {
	db     DBTX
	conn   *sql.DB
	driver jokadb.Driver
}

func NewPostgresDBAdapter(conn *sql.DB) *PostgresDBAdapter {
	return &PostgresDBAdapter{db: conn, conn: conn, driver: jokadb.Postgres}
}

func NewPostgresTxDBAdapter(tx *sql.Tx, conn *sql.DB) *PostgresDBAdapter {
	return &PostgresDBAdapter{db: tx, conn: conn, driver: jokadb.Postgres}
}

func (p *PostgresDBAdapter) TruncateTable(ctx context.Context, tableName string) error {
	exists, err := jokadb.TableExists(ctx, p.conn, p.driver, tableName)
	if err != nil {
		return err
	}
	if !exists {
		return fmt.Errorf("%w: %s", domain.ErrTableNotFound, tableName)
	}

	_, err = p.db.ExecContext(ctx, fmt.Sprintf(`TRUNCATE TABLE "%s" RESTART IDENTITY`, tableName))
	return err
}

func (p *PostgresDBAdapter) InsertRows(ctx context.Context, tableName string, rows []map[string]any) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	exists, err := jokadb.TableExists(ctx, p.conn, p.driver, tableName)
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
		colNames[i] = fmt.Sprintf(`"%s"`, c)
		placeholders[i] = fmt.Sprintf("$%d", i+1)
	}

	query := fmt.Sprintf(`INSERT INTO "%s" (%s) VALUES (%s)`,
		tableName,
		strings.Join(colNames, ", "),
		strings.Join(placeholders, ", "),
	)

	for _, row := range rows {
		args := make([]any, len(columns))
		for i, c := range columns {
			args[i] = row[c]
		}
		if _, err := p.db.ExecContext(ctx, query, args...); err != nil {
			return 0, err
		}
	}

	return len(rows), nil
}
