package infra

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/domains/migration/domain"
	"github.com/apsdsm/joka/internal/domains/migration/infra/models"
)

// DBTX is the minimal interface shared by *sql.DB and *sql.Tx, allowing the
// adapter to run queries against either a raw connection or inside a transaction.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// MySQLDBAdapter implements the app.DBAdapter interface for MySQL databases.
// It holds both a DBTX (which may be a transaction) for running queries and
// a raw *sql.DB connection for operations that must run outside a transaction
// (e.g. DDL statements like CREATE TABLE, or information_schema lookups).
type MySQLDBAdapter struct {
	db   DBTX   // query executor — either *sql.DB or *sql.Tx
	conn *sql.DB // raw connection, always available for DDL and metadata
}

// NewMySQLDBAdapter creates a new MySQLDBAdapter using a direct database connection.
func NewMySQLDBAdapter(conn *sql.DB) *MySQLDBAdapter {
	return &MySQLDBAdapter{db: conn, conn: conn}
}

// NewMySQLTxDBAdapter creates a new MySQLDBAdapter using a transaction.
func NewMySQLTxDBAdapter(tx *sql.Tx, conn *sql.DB) *MySQLDBAdapter {
	return &MySQLDBAdapter{db: tx, conn: conn}
}

// GetAppliedMigrations retrieves the list of applied migrations from the database.
func (m *MySQLDBAdapter) GetAppliedMigrations(ctx context.Context) ([]models.MigrationRow, error) {
	exists, err := m.HasMigrationsTable(ctx)

	if err != nil {
		return nil, err
	}

	if !exists {
		return nil, domain.ErrNoMigrationTable
	}

	rows, err := m.db.QueryContext(ctx, `SELECT id, migration_index, applied_at FROM joka_migrations ORDER BY id`)

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
func (m *MySQLDBAdapter) ApplySQLFromFile(ctx context.Context, filePath string) error {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("reading migration file: %w", err)
	}

	sqlContent := string(content)
	if strings.TrimSpace(sqlContent) == "" {
		return nil
	}

	_, err = m.db.ExecContext(ctx, sqlContent)
	return err
}

// RecordMigrationApplied records a migration as applied in the migrations table.
func (m *MySQLDBAdapter) RecordMigrationApplied(ctx context.Context, migrationIndex string) error {
	_, err := m.db.ExecContext(ctx, `INSERT INTO joka_migrations (migration_index) VALUES (?)`, migrationIndex)
	return err
}

// HasMigrationsTable checks if the migrations table exists in the database.
func (m *MySQLDBAdapter) HasMigrationsTable(ctx context.Context) (bool, error) {
	return db.TableExists(ctx, m.conn, "joka_migrations")
}

// CreateMigrationsTable creates the migrations table if it does not already exist.
func (m *MySQLDBAdapter) CreateMigrationsTable(ctx context.Context) error {
	exists, err := m.HasMigrationsTable(ctx)
	if err != nil {
		return err
	}
	if exists {
		return domain.ErrMigrationAlreadyExists
	}

	_, err = m.conn.ExecContext(ctx, `
		CREATE TABLE joka_migrations (
			id INT AUTO_INCREMENT PRIMARY KEY,
			migration_index VARCHAR(255) NOT NULL UNIQUE,
			applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("%w: %v", domain.ErrMigrationTableCreation, err)
	}
	return nil
}

// EnsureSnapshotsTable creates the joka_snapshots table if it doesn't already
// exist. Called automatically before any snapshot read/write so callers don't
// need to run a separate init step.
func (m *MySQLDBAdapter) EnsureSnapshotsTable(ctx context.Context) error {
	exists, err := db.TableExists(ctx, m.conn, "joka_snapshots")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	_, err = m.conn.ExecContext(ctx, `
		CREATE TABLE joka_snapshots (
			id INT AUTO_INCREMENT PRIMARY KEY,
			migration_index VARCHAR(255) NOT NULL UNIQUE,
			schema_snapshot LONGTEXT NOT NULL,
			captured_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	return err
}

// CaptureSchemaSnapshot captures the current database schema and stores it
// associated with the given migration index. It queries SHOW CREATE TABLE for
// all user tables (excluding joka_* tables).
func (m *MySQLDBAdapter) CaptureSchemaSnapshot(ctx context.Context, migrationIndex string) error {
	if err := m.EnsureSnapshotsTable(ctx); err != nil {
		return fmt.Errorf("ensuring snapshots table: %w", err)
	}

	rows, err := m.conn.QueryContext(ctx, `
		SELECT table_name
		FROM information_schema.tables
		WHERE table_schema = DATABASE()
		AND table_name NOT LIKE 'joka\_%'
		ORDER BY table_name
	`)
	if err != nil {
		return fmt.Errorf("listing tables: %w", err)
	}
	defer rows.Close()

	// Phase 1: collect all user table names (excluding joka_* internal tables).
	schema := make(map[string]string)
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

	// Phase 2: get the full CREATE TABLE statement for each table.
	for _, name := range tableNames {
		var tbl, createStmt string
		err := m.conn.QueryRowContext(ctx, fmt.Sprintf("SHOW CREATE TABLE `%s`", name)).Scan(&tbl, &createStmt)
		if err != nil {
			return fmt.Errorf("getting schema for table %s: %w", name, err)
		}
		schema[name] = createStmt
	}

	// Store the schema as a JSON object: {"table_name": "CREATE TABLE ...", ...}
	jsonBytes, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("marshaling schema: %w", err)
	}

	_, err = m.conn.ExecContext(ctx,
		`INSERT INTO joka_snapshots (migration_index, schema_snapshot) VALUES (?, ?)`,
		migrationIndex, string(jsonBytes),
	)
	return err
}

// GetSchemaSnapshot retrieves the stored schema snapshot for a given migration index.
func (m *MySQLDBAdapter) GetSchemaSnapshot(ctx context.Context, migrationIndex string) (string, error) {
	if err := m.EnsureSnapshotsTable(ctx); err != nil {
		return "", fmt.Errorf("ensuring snapshots table: %w", err)
	}

	var snapshot string
	err := m.conn.QueryRowContext(ctx,
		`SELECT schema_snapshot FROM joka_snapshots WHERE migration_index = ?`,
		migrationIndex,
	).Scan(&snapshot)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("no snapshot found for migration %s", migrationIndex)
	}
	return snapshot, err
}

// GetLatestSnapshotIndex returns the migration index of the most recent snapshot.
func (m *MySQLDBAdapter) GetLatestSnapshotIndex(ctx context.Context) (string, error) {
	if err := m.EnsureSnapshotsTable(ctx); err != nil {
		return "", fmt.Errorf("ensuring snapshots table: %w", err)
	}

	var index string
	err := m.conn.QueryRowContext(ctx,
		`SELECT migration_index FROM joka_snapshots ORDER BY id DESC LIMIT 1`,
	).Scan(&index)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("no snapshots found")
	}
	return index, err
}
