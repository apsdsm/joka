package infra

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/lib/pq"

	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// PostgresDBAdapter implements entity app.DBAdapter for PostgreSQL.
type PostgresDBAdapter struct {
	db   DBTX
	conn *sql.DB
}

// NewPostgresDBAdapter creates an adapter that runs all queries on the raw connection.
func NewPostgresDBAdapter(conn *sql.DB) *PostgresDBAdapter {
	return &PostgresDBAdapter{db: conn, conn: conn}
}

// NewPostgresTxDBAdapter creates an adapter that runs InsertRow inside the
// given transaction, while tracking-table DDL uses the raw connection.
func NewPostgresTxDBAdapter(tx *sql.Tx, conn *sql.DB) *PostgresDBAdapter {
	return &PostgresDBAdapter{db: tx, conn: conn}
}

// EnsureTrackingTable creates the joka_entities table if it does not already
// exist.
//
// Legacy: tracking version 3 moved entity tracking into the joka_state
// document and dropped this table. Nothing creates it any more — it is kept so
// the upgrade's tests can build a database at the version the upgrade reads.
func (p *PostgresDBAdapter) EnsureTrackingTable(ctx context.Context) error {
	exists, err := jokadb.TableExists(ctx, p.conn, "joka_entities")
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

// InsertRow inserts a single row into the given table. For natural-key tables
// (where pkColumn is present in columns), the value from the columns map is
// returned so reimport can locate the row later. Otherwise PostgreSQL's
// RETURNING clause is used to fetch the inserted row's auto-generated id
// (since LastInsertId is not supported by lib/pq).
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

	pkVal, pkPresent, pkErr := pkValueFromColumns(columns, pkColumn)
	if pkErr != nil {
		return 0, fmt.Errorf("inserting into %s: %w", table, pkErr)
	}

	if pkPresent {
		query := fmt.Sprintf(`INSERT INTO "%s" (%s) VALUES (%s)`,
			table,
			strings.Join(colNames, ", "),
			strings.Join(placeholders, ", "),
		)
		if _, err := p.db.ExecContext(ctx, query, args...); err != nil {
			return 0, fmt.Errorf("inserting into %s: %w", table, err)
		}
		return pkVal, nil
	}

	if pkColumn != "id" {
		return 0, fmt.Errorf("inserting into %s: _pk column %q is not present in entity columns", table, pkColumn)
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

// UpdateRow updates a single existing row, matched by pkColumn = pkValue,
// setting every column in the map except the primary key column itself.
func (p *PostgresDBAdapter) UpdateRow(ctx context.Context, table, pkColumn string, pkValue int64, columns map[string]any) error {
	sets := make([]string, 0, len(columns))
	args := make([]any, 0, len(columns)+1)

	i := 1
	for k, v := range columns {
		if k == pkColumn {
			continue
		}
		sets = append(sets, fmt.Sprintf(`"%s" = $%d`, k, i))
		args = append(args, v)
		i++
	}

	if len(sets) == 0 {
		return nil // nothing to update beyond the primary key
	}

	args = append(args, pkValue)

	query := fmt.Sprintf(`UPDATE "%s" SET %s WHERE "%s" = $%d`,
		table,
		strings.Join(sets, ", "),
		pkColumn,
		i,
	)

	if _, err := p.db.ExecContext(ctx, query, args...); err != nil {
		return fmt.Errorf("updating %s: %w", table, err)
	}

	return nil
}

// GetRow reads the given columns from a single row matched by pkColumn =
// pkValue. Byte-slice values are converted to strings so callers can compare
// them against resolved values.
func (p *PostgresDBAdapter) GetRow(ctx context.Context, table string, columns []string, pkColumn string, pkValue int64) (map[string]any, error) {
	result := make(map[string]any, len(columns))
	if len(columns) == 0 {
		return result, nil
	}

	quoted := make([]string, len(columns))
	for i, c := range columns {
		quoted[i] = fmt.Sprintf(`"%s"`, c)
	}

	query := fmt.Sprintf(`SELECT %s FROM "%s" WHERE "%s" = $1 LIMIT 1`,
		strings.Join(quoted, ", "), table, pkColumn)

	dest := make([]any, len(columns))
	scan := make([]any, len(columns))
	for i := range dest {
		scan[i] = &dest[i]
	}

	err := p.db.QueryRowContext(ctx, query, pkValue).Scan(scan...)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("reading %s row %s=%d: row not found", table, pkColumn, pkValue)
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s row %s=%d: %w", table, pkColumn, pkValue, err)
	}

	for i, c := range columns {
		if b, ok := dest[i].([]byte); ok {
			result[c] = string(b)
		} else {
			result[c] = dest[i]
		}
	}

	return result, nil
}

// LookupValue queries a single value from an existing table row. Returns
// ErrLookupNotFound if no matching row exists.
func (p *PostgresDBAdapter) LookupValue(ctx context.Context, table, returnCol, whereCol string, whereVal any) (any, error) {
	query := fmt.Sprintf(`SELECT "%s" FROM "%s" WHERE "%s" = $1 LIMIT 1`, returnCol, table, whereCol)

	var result any

	err := p.db.QueryRowContext(ctx, query, whereVal).Scan(&result)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("%w: %s.%s where %s=%v", domain.ErrLookupNotFound, table, returnCol, whereCol, whereVal)
	}

	if err != nil {
		return nil, fmt.Errorf("lookup %s.%s: %w", table, returnCol, err)
	}

	return result, nil
}

// RefIDIndex is the unique index that makes _id the identity of a tracked row.
// Named so the upgrade step and the fresh-table path agree on it.
const RefIDIndex = "joka_entity_rows_ref_id_key"

// EnsureRowTrackingTable creates the joka_entity_rows table if it does not
// already exist.
//
// A table created now gets the unique index on ref_id with it. A table that
// already exists is left alone: adding the constraint to one whose rows predate
// it can fail, so that path belongs to the upgrade (internal/upgrade), which
// checks the data first and reports what stands in the way.
func (p *PostgresDBAdapter) EnsureRowTrackingTable(ctx context.Context) error {
	exists, err := jokadb.TableExists(ctx, p.conn, "joka_entity_rows")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	if _, err := p.conn.ExecContext(ctx, `
		CREATE TABLE joka_entity_rows (
			id BIGSERIAL PRIMARY KEY,
			entity_file VARCHAR(512) NOT NULL,
			table_name VARCHAR(255) NOT NULL,
			row_pk BIGINT NOT NULL,
			pk_column VARCHAR(255) NOT NULL DEFAULT 'id',
			ref_id VARCHAR(255) NOT NULL,
			insertion_order INT NOT NULL
		)
	`); err != nil {
		return err
	}

	return p.EnsureRefIDIndex(ctx)
}

// EnsureRefIDIndex adds the unique index on ref_id if it is not already there.
// It fails on data that breaks the constraint, so callers working on an
// existing table check first.
func (p *PostgresDBAdapter) EnsureRefIDIndex(ctx context.Context) error {
	_, err := p.conn.ExecContext(ctx,
		`CREATE UNIQUE INDEX IF NOT EXISTS `+RefIDIndex+` ON joka_entity_rows (ref_id)`)
	if err != nil {
		return fmt.Errorf("adding the unique index on ref_id: %w", err)
	}
	return nil
}

// EnsureContentHashColumn adds the content_hash column to joka_entities if
// it is not already present.
func (p *PostgresDBAdapter) EnsureContentHashColumn(ctx context.Context) error {
	var col string
	err := p.conn.QueryRowContext(ctx,
		`SELECT column_name FROM information_schema.columns
		 WHERE table_name = 'joka_entities' AND column_name = 'content_hash'`,
	).Scan(&col)
	if err == nil {
		return nil // column already exists
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("checking content_hash column: %w", err)
	}

	_, err = p.conn.ExecContext(ctx,
		`ALTER TABLE joka_entities ADD COLUMN content_hash VARCHAR(64)`,
	)
	return err
}

// DeleteRow deletes a single row from the given table by primary key. Returns
// ErrForeignKeyConflict if a FK constraint blocks the deletion.
func (p *PostgresDBAdapter) DeleteRow(ctx context.Context, table, pkColumn string, pkValue int64) error {
	_, err := p.db.ExecContext(ctx,
		fmt.Sprintf(`DELETE FROM "%s" WHERE "%s" = $1`, table, pkColumn),
		pkValue,
	)
	if err != nil {
		var pqErr *pq.Error
		if errors.As(err, &pqErr) && pqErr.Code == "23503" {
			return fmt.Errorf("%w: table %s, %s=%d: %s", domain.ErrForeignKeyConflict, table, pkColumn, pkValue, pqErr.Message)
		}
		return fmt.Errorf("deleting from %s: %w", table, err)
	}
	return nil
}

// TableExists reports whether the named table is present in the current schema.
func (p *PostgresDBAdapter) TableExists(ctx context.Context, table string) (bool, error) {
	return jokadb.TableExists(ctx, p.conn, table)
}

// RowExists reports whether a single row is still present, matched by
// pkColumn = pkValue.
func (p *PostgresDBAdapter) RowExists(ctx context.Context, table, pkColumn string, pkValue int64) (bool, error) {
	var one int
	err := p.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT 1 FROM "%s" WHERE "%s" = $1 LIMIT 1`, table, pkColumn),
		pkValue,
	).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("checking for %s.%s = %d: %w", table, pkColumn, pkValue, err)
	}
	return true, nil
}
