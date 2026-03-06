package infra

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"

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

// EnsureRowTrackingTable creates the joka_entity_rows table if it does not
// already exist.
func (m *MySQLDBAdapter) EnsureRowTrackingTable(ctx context.Context) error {
	exists, err := jokadb.TableExists(ctx, m.conn, m.driver, "joka_entity_rows")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	_, err = m.conn.ExecContext(ctx, `
		CREATE TABLE joka_entity_rows (
			id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
			entity_file VARCHAR(512) NOT NULL,
			table_name VARCHAR(255) NOT NULL,
			row_pk BIGINT NOT NULL,
			pk_column VARCHAR(255) NOT NULL DEFAULT 'id',
			ref_id VARCHAR(255),
			insertion_order INT NOT NULL
		)
	`)
	return err
}

// EnsureContentHashColumn adds the content_hash column to joka_entities if
// it is not already present.
func (m *MySQLDBAdapter) EnsureContentHashColumn(ctx context.Context) error {
	var col string
	err := m.conn.QueryRowContext(ctx,
		`SHOW COLUMNS FROM joka_entities LIKE 'content_hash'`,
	).Scan(&col, new(string), new(string), new(string), new(sql.NullString), new(string))
	if err == nil {
		return nil // column already exists
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("checking content_hash column: %w", err)
	}

	_, err = m.conn.ExecContext(ctx,
		`ALTER TABLE joka_entities ADD COLUMN content_hash VARCHAR(64)`,
	)
	return err
}

// RecordEntitySyncedWithHash inserts a row into joka_entities with a content
// hash for change detection.
func (m *MySQLDBAdapter) RecordEntitySyncedWithHash(ctx context.Context, filePath, contentHash string) error {
	_, err := m.conn.ExecContext(ctx,
		`INSERT INTO joka_entities (entity_file, content_hash) VALUES (?, ?)
		 ON DUPLICATE KEY UPDATE content_hash = VALUES(content_hash), synced_at = CURRENT_TIMESTAMP`,
		filePath, contentHash,
	)
	return err
}

// UpdateEntitySynced updates an existing joka_entities row with a new content
// hash and synced_at timestamp.
func (m *MySQLDBAdapter) UpdateEntitySynced(ctx context.Context, filePath, contentHash string) error {
	_, err := m.conn.ExecContext(ctx,
		`UPDATE joka_entities SET content_hash = ?, synced_at = CURRENT_TIMESTAMP WHERE entity_file = ?`,
		contentHash, filePath,
	)
	return err
}

// GetEntityHash returns the content_hash stored for a synced entity file.
func (m *MySQLDBAdapter) GetEntityHash(ctx context.Context, filePath string) (string, error) {
	var hash sql.NullString
	err := m.conn.QueryRowContext(ctx,
		`SELECT content_hash FROM joka_entities WHERE entity_file = ?`,
		filePath,
	).Scan(&hash)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("getting entity hash: %w", err)
	}
	return hash.String, nil
}

// GetAllSyncedEntities returns all entity_file paths mapped to content hashes.
func (m *MySQLDBAdapter) GetAllSyncedEntities(ctx context.Context) (map[string]string, error) {
	rows, err := m.conn.QueryContext(ctx,
		`SELECT entity_file, content_hash FROM joka_entities`,
	)
	if err != nil {
		return nil, fmt.Errorf("querying synced entities: %w", err)
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var file string
		var hash sql.NullString
		if err := rows.Scan(&file, &hash); err != nil {
			return nil, err
		}
		result[file] = hash.String
	}
	return result, rows.Err()
}

// RecordEntityRow inserts a row into joka_entity_rows to track an individual
// inserted entity row.
func (m *MySQLDBAdapter) RecordEntityRow(ctx context.Context, row domain.TrackedRow) error {
	_, err := m.db.ExecContext(ctx,
		`INSERT INTO joka_entity_rows (entity_file, table_name, row_pk, pk_column, ref_id, insertion_order)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		row.EntityFile, row.TableName, row.RowPK, row.PKColumn, nullString(row.RefID), row.InsertionOrder,
	)
	return err
}

// GetTrackedRows returns all tracked rows for a given entity file in reverse
// insertion order (for deletion).
func (m *MySQLDBAdapter) GetTrackedRows(ctx context.Context, entityFile string) ([]domain.TrackedRow, error) {
	rows, err := m.conn.QueryContext(ctx,
		`SELECT entity_file, table_name, row_pk, pk_column, ref_id, insertion_order
		 FROM joka_entity_rows WHERE entity_file = ? ORDER BY insertion_order DESC`,
		entityFile,
	)
	if err != nil {
		return nil, fmt.Errorf("querying tracked rows: %w", err)
	}
	defer rows.Close()

	var result []domain.TrackedRow
	for rows.Next() {
		var r domain.TrackedRow
		var refID sql.NullString
		if err := rows.Scan(&r.EntityFile, &r.TableName, &r.RowPK, &r.PKColumn, &refID, &r.InsertionOrder); err != nil {
			return nil, err
		}
		r.RefID = refID.String
		result = append(result, r)
	}
	return result, rows.Err()
}

// DeleteTrackedRows removes all joka_entity_rows entries for a given entity file.
func (m *MySQLDBAdapter) DeleteTrackedRows(ctx context.Context, entityFile string) error {
	_, err := m.db.ExecContext(ctx,
		`DELETE FROM joka_entity_rows WHERE entity_file = ?`,
		entityFile,
	)
	return err
}

// DeleteRow deletes a single row from the given table by primary key. Returns
// ErrForeignKeyConflict if a FK constraint blocks the deletion.
func (m *MySQLDBAdapter) DeleteRow(ctx context.Context, table, pkColumn string, pkValue int64) error {
	_, err := m.db.ExecContext(ctx,
		fmt.Sprintf("DELETE FROM `%s` WHERE `%s` = ?", table, pkColumn),
		pkValue,
	)
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1451 {
			return fmt.Errorf("%w: table %s, %s=%d: %s", domain.ErrForeignKeyConflict, table, pkColumn, pkValue, mysqlErr.Message)
		}
		return fmt.Errorf("deleting from %s: %w", table, err)
	}
	return nil
}

// DeleteEntityRecord removes the joka_entities row for a given file path.
func (m *MySQLDBAdapter) DeleteEntityRecord(ctx context.Context, filePath string) error {
	_, err := m.conn.ExecContext(ctx,
		`DELETE FROM joka_entities WHERE entity_file = ?`,
		filePath,
	)
	return err
}

// nullString converts an empty string to a sql.NullString{Valid: false}.
func nullString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
