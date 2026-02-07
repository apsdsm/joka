package infra

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	"github.com/nickfiggins/joka/db"
	"github.com/nickfiggins/joka/internal/domains/migration/domain"
	"github.com/nickfiggins/joka/internal/domains/migration/infra/models"
)

// DBAdapter defines the methods required to interact with the database for
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// MySQLDBAdapter implements the DBAdapter interface for MySQL databases.
type MySQLDBAdapter struct {
	db   DBTX
	conn *sql.DB
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

	rows, err := m.db.QueryContext(ctx, `SELECT id, migration_index, applied_at FROM migrations ORDER BY id`)

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
	_, err := m.db.ExecContext(ctx, `INSERT INTO migrations (migration_index) VALUES (?)`, migrationIndex)
	return err
}

// HasMigrationsTable checks if the migrations table exists in the database.
func (m *MySQLDBAdapter) HasMigrationsTable(ctx context.Context) (bool, error) {
	return db.TableExists(ctx, m.conn, "migrations")
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
		CREATE TABLE migrations (
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
