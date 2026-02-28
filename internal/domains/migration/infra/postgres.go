package infra

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/domains/migration/domain"
	"github.com/apsdsm/joka/internal/domains/migration/infra/models"
)

// PostgresDBAdapter implements the app.DBAdapter interface for PostgreSQL databases.
type PostgresDBAdapter struct {
	db     DBTX
	conn   *sql.DB
	driver jokadb.Driver
}

// NewPostgresDBAdapter creates a new PostgresDBAdapter using a direct database connection.
func NewPostgresDBAdapter(conn *sql.DB) *PostgresDBAdapter {
	return &PostgresDBAdapter{db: conn, conn: conn, driver: jokadb.Postgres}
}

// NewPostgresTxDBAdapter creates a new PostgresDBAdapter using a transaction.
func NewPostgresTxDBAdapter(tx *sql.Tx, conn *sql.DB) *PostgresDBAdapter {
	return &PostgresDBAdapter{db: tx, conn: conn, driver: jokadb.Postgres}
}

// GetAppliedMigrations retrieves the list of applied migrations from the database.
func (p *PostgresDBAdapter) GetAppliedMigrations(ctx context.Context) ([]models.MigrationRow, error) {
	exists, err := p.HasMigrationsTable(ctx)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, domain.ErrNoMigrationTable
	}

	rows, err := p.db.QueryContext(ctx, `SELECT id, migration_index, applied_at FROM joka_migrations ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var migrations []models.MigrationRow
	for rows.Next() {
		var mr models.MigrationRow
		if err := rows.Scan(&mr.ID, &mr.MigrationIndex, &mr.AppliedAt); err != nil {
			return nil, err
		}
		migrations = append(migrations, mr)
	}
	return migrations, rows.Err()
}

// ApplySQLFromFile reads and executes the SQL statements from the specified file.
func (p *PostgresDBAdapter) ApplySQLFromFile(ctx context.Context, filePath string) error {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("reading migration file: %w", err)
	}

	sqlContent := string(content)
	if strings.TrimSpace(sqlContent) == "" {
		return nil
	}

	_, err = p.db.ExecContext(ctx, sqlContent)
	return err
}

// RecordMigrationApplied records a migration as applied in the migrations table.
func (p *PostgresDBAdapter) RecordMigrationApplied(ctx context.Context, migrationIndex string) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO joka_migrations (migration_index) VALUES ($1)`, migrationIndex)
	return err
}

// HasMigrationsTable checks if the migrations table exists in the database.
func (p *PostgresDBAdapter) HasMigrationsTable(ctx context.Context) (bool, error) {
	return jokadb.TableExists(ctx, p.conn, p.driver, "joka_migrations")
}

// CreateMigrationsTable creates the migrations table if it does not already exist.
func (p *PostgresDBAdapter) CreateMigrationsTable(ctx context.Context) error {
	exists, err := p.HasMigrationsTable(ctx)
	if err != nil {
		return err
	}
	if exists {
		return domain.ErrMigrationAlreadyExists
	}

	_, err = p.conn.ExecContext(ctx, `
		CREATE TABLE joka_migrations (
			id SERIAL PRIMARY KEY,
			migration_index VARCHAR(255) NOT NULL UNIQUE,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	if err != nil {
		return fmt.Errorf("%w: %v", domain.ErrMigrationTableCreation, err)
	}
	return nil
}

// EnsureSnapshotsTable creates the joka_snapshots table if it doesn't already exist.
func (p *PostgresDBAdapter) EnsureSnapshotsTable(ctx context.Context) error {
	exists, err := jokadb.TableExists(ctx, p.conn, p.driver, "joka_snapshots")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	_, err = p.conn.ExecContext(ctx, `
		CREATE TABLE joka_snapshots (
			id SERIAL PRIMARY KEY,
			migration_index VARCHAR(255) NOT NULL UNIQUE,
			schema_snapshot TEXT NOT NULL,
			captured_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	return err
}

// CaptureSchemaSnapshot captures the current database schema and stores it
// associated with the given migration index. For PostgreSQL, it queries
// information_schema for column definitions of all non-joka user tables.
func (p *PostgresDBAdapter) CaptureSchemaSnapshot(ctx context.Context, migrationIndex string) error {
	if err := p.EnsureSnapshotsTable(ctx); err != nil {
		return fmt.Errorf("ensuring snapshots table: %w", err)
	}

	// Get all non-joka tables in the current schema.
	rows, err := p.conn.QueryContext(ctx, `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = current_schema()
		AND table_name NOT LIKE 'joka\_%' ESCAPE '\'
		ORDER BY table_name
	`)
	if err != nil {
		return fmt.Errorf("listing tables: %w", err)
	}
	defer rows.Close()

	var tableNames []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		tableNames = append(tableNames, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// For each table, reconstruct a CREATE TABLE-like statement from information_schema.
	schema := make(map[string]string)
	for _, name := range tableNames {
		stmt, err := p.reconstructCreateTable(ctx, name)
		if err != nil {
			return fmt.Errorf("getting schema for table %s: %w", name, err)
		}
		schema[name] = stmt
	}

	jsonBytes, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("marshaling schema: %w", err)
	}

	_, err = p.conn.ExecContext(ctx,
		`INSERT INTO joka_snapshots (migration_index, schema_snapshot) VALUES ($1, $2)`,
		migrationIndex, string(jsonBytes),
	)
	return err
}

// reconstructCreateTable builds a pseudo-CREATE TABLE statement from
// information_schema and pg_catalog for snapshot purposes. Includes columns,
// primary keys, unique constraints, foreign keys, and indexes.
func (p *PostgresDBAdapter) reconstructCreateTable(ctx context.Context, tableName string) (string, error) {
	// 1. Columns
	colRows, err := p.conn.QueryContext(ctx, `
		SELECT column_name, data_type, is_nullable, column_default,
		       character_maximum_length, numeric_precision, numeric_scale
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		AND table_name = $1
		ORDER BY ordinal_position
	`, tableName)
	if err != nil {
		return "", err
	}
	defer colRows.Close()

	var parts []string
	for colRows.Next() {
		var colName, dataType, isNullable string
		var colDefault sql.NullString
		var charMaxLen, numPrecision, numScale sql.NullInt64

		if err := colRows.Scan(&colName, &dataType, &isNullable, &colDefault, &charMaxLen, &numPrecision, &numScale); err != nil {
			return "", err
		}

		col := fmt.Sprintf("  %s %s", colName, dataType)
		if charMaxLen.Valid {
			col += fmt.Sprintf("(%d)", charMaxLen.Int64)
		} else if numPrecision.Valid && dataType == "numeric" {
			if numScale.Valid && numScale.Int64 > 0 {
				col += fmt.Sprintf("(%d,%d)", numPrecision.Int64, numScale.Int64)
			} else {
				col += fmt.Sprintf("(%d)", numPrecision.Int64)
			}
		}
		if isNullable == "NO" {
			col += " NOT NULL"
		}
		if colDefault.Valid {
			col += fmt.Sprintf(" DEFAULT %s", colDefault.String)
		}
		parts = append(parts, col)
	}
	if err := colRows.Err(); err != nil {
		return "", err
	}

	// 2. Table constraints (primary keys, unique, foreign keys)
	conRows, err := p.conn.QueryContext(ctx, `
		SELECT
			c.conname,
			c.contype,
			pg_get_constraintdef(c.oid, true) AS def
		FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE t.relname = $1
		AND n.nspname = current_schema()
		ORDER BY
			CASE c.contype WHEN 'p' THEN 0 WHEN 'u' THEN 1 WHEN 'f' THEN 2 ELSE 3 END,
			c.conname
	`, tableName)
	if err != nil {
		return "", err
	}
	defer conRows.Close()

	for conRows.Next() {
		var conName, conType, conDef string
		if err := conRows.Scan(&conName, &conType, &conDef); err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("  CONSTRAINT %s %s", conName, conDef))
	}
	if err := conRows.Err(); err != nil {
		return "", err
	}

	result := fmt.Sprintf("CREATE TABLE %s (\n%s\n)", tableName, strings.Join(parts, ",\n"))

	// 3. Indexes (exclude those backing constraints — already covered above)
	idxRows, err := p.conn.QueryContext(ctx, `
		SELECT indexname, indexdef
		FROM pg_indexes
		WHERE schemaname = current_schema()
		AND tablename = $1
		AND indexname NOT IN (
			SELECT conname FROM pg_constraint c
			JOIN pg_class t ON t.oid = c.conrelid
			JOIN pg_namespace n ON n.oid = t.relnamespace
			WHERE t.relname = $1 AND n.nspname = current_schema()
		)
		ORDER BY indexname
	`, tableName)
	if err != nil {
		return "", err
	}
	defer idxRows.Close()

	for idxRows.Next() {
		var idxName, idxDef string
		if err := idxRows.Scan(&idxName, &idxDef); err != nil {
			return "", err
		}
		result += fmt.Sprintf("\n%s;", idxDef)
	}
	if err := idxRows.Err(); err != nil {
		return "", err
	}

	return result, nil
}

// GetSchemaSnapshot retrieves the stored schema snapshot for a given migration index.
func (p *PostgresDBAdapter) GetSchemaSnapshot(ctx context.Context, migrationIndex string) (string, error) {
	if err := p.EnsureSnapshotsTable(ctx); err != nil {
		return "", fmt.Errorf("ensuring snapshots table: %w", err)
	}

	var snapshot string
	err := p.conn.QueryRowContext(ctx,
		`SELECT schema_snapshot FROM joka_snapshots WHERE migration_index = $1`,
		migrationIndex,
	).Scan(&snapshot)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("no snapshot found for migration %s", migrationIndex)
	}
	return snapshot, err
}

// GetLatestSnapshotIndex returns the migration index of the most recent snapshot.
func (p *PostgresDBAdapter) GetLatestSnapshotIndex(ctx context.Context) (string, error) {
	if err := p.EnsureSnapshotsTable(ctx); err != nil {
		return "", fmt.Errorf("ensuring snapshots table: %w", err)
	}

	var index string
	err := p.conn.QueryRowContext(ctx,
		`SELECT migration_index FROM joka_snapshots ORDER BY id DESC LIMIT 1`,
	).Scan(&index)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("no snapshots found")
	}
	return index, err
}
