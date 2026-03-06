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

// EnsureRowTrackingTable creates the joka_entity_rows table if it does not
// already exist.
func (p *PostgresDBAdapter) EnsureRowTrackingTable(ctx context.Context) error {
	exists, err := jokadb.TableExists(ctx, p.conn, p.driver, "joka_entity_rows")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	_, err = p.conn.ExecContext(ctx, `
		CREATE TABLE joka_entity_rows (
			id BIGSERIAL PRIMARY KEY,
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

// RecordEntitySyncedWithHash inserts a row into joka_entities with a content
// hash for change detection.
func (p *PostgresDBAdapter) RecordEntitySyncedWithHash(ctx context.Context, filePath, contentHash string) error {
	_, err := p.conn.ExecContext(ctx,
		`INSERT INTO joka_entities (entity_file, content_hash) VALUES ($1, $2)
		 ON CONFLICT (entity_file) DO UPDATE SET content_hash = EXCLUDED.content_hash, synced_at = NOW()`,
		filePath, contentHash,
	)
	return err
}

// UpdateEntitySynced updates an existing joka_entities row with a new content
// hash and synced_at timestamp.
func (p *PostgresDBAdapter) UpdateEntitySynced(ctx context.Context, filePath, contentHash string) error {
	_, err := p.conn.ExecContext(ctx,
		`UPDATE joka_entities SET content_hash = $1, synced_at = NOW() WHERE entity_file = $2`,
		contentHash, filePath,
	)
	return err
}

// GetEntityHash returns the content_hash stored for a synced entity file.
func (p *PostgresDBAdapter) GetEntityHash(ctx context.Context, filePath string) (string, error) {
	var hash sql.NullString
	err := p.conn.QueryRowContext(ctx,
		`SELECT content_hash FROM joka_entities WHERE entity_file = $1`,
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
func (p *PostgresDBAdapter) GetAllSyncedEntities(ctx context.Context) (map[string]string, error) {
	rows, err := p.conn.QueryContext(ctx,
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
func (p *PostgresDBAdapter) RecordEntityRow(ctx context.Context, row domain.TrackedRow) error {
	_, err := p.db.ExecContext(ctx,
		`INSERT INTO joka_entity_rows (entity_file, table_name, row_pk, pk_column, ref_id, insertion_order)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		row.EntityFile, row.TableName, row.RowPK, row.PKColumn, nullString(row.RefID), row.InsertionOrder,
	)
	return err
}

// GetTrackedRows returns all tracked rows for a given entity file in reverse
// insertion order (for deletion).
func (p *PostgresDBAdapter) GetTrackedRows(ctx context.Context, entityFile string) ([]domain.TrackedRow, error) {
	rows, err := p.conn.QueryContext(ctx,
		`SELECT entity_file, table_name, row_pk, pk_column, ref_id, insertion_order
		 FROM joka_entity_rows WHERE entity_file = $1 ORDER BY insertion_order DESC`,
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
func (p *PostgresDBAdapter) DeleteTrackedRows(ctx context.Context, entityFile string) error {
	_, err := p.db.ExecContext(ctx,
		`DELETE FROM joka_entity_rows WHERE entity_file = $1`,
		entityFile,
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

// DeleteEntityRecord removes the joka_entities row for a given file path.
func (p *PostgresDBAdapter) DeleteEntityRecord(ctx context.Context, filePath string) error {
	_, err := p.conn.ExecContext(ctx,
		`DELETE FROM joka_entities WHERE entity_file = $1`,
		filePath,
	)
	return err
}
