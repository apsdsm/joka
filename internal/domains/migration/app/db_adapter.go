package app

import (
	"context"

	"github.com/apsdsm/joka/internal/domains/migration/infra/models"
)

// DBAdapter defines the database contract for migration operations. It is
// implemented by infra.MySQLDBAdapter and can be backed by either a raw
// connection or a transaction.
type DBAdapter interface {
	// HasMigrationsTable returns true if the joka_migrations table exists.
	HasMigrationsTable(ctx context.Context) (bool, error)
	// CreateMigrationsTable creates the joka_migrations table. Returns an error
	// if the table already exists.
	CreateMigrationsTable(ctx context.Context) error
	// GetAppliedMigrations returns all rows from joka_migrations ordered by id.
	GetAppliedMigrations(ctx context.Context) ([]models.MigrationRow, error)
	// ApplySQLFromFile reads and executes the SQL from the given file path.
	ApplySQLFromFile(ctx context.Context, filePath string) error
	// RecordMigrationApplied inserts a row into joka_migrations for the given index.
	RecordMigrationApplied(ctx context.Context, migrationIndex string) error
	// EnsureSnapshotsTable creates the joka_snapshots table if it doesn't exist.
	EnsureSnapshotsTable(ctx context.Context) error
	// CaptureSchemaSnapshot records the full database schema (all non-joka tables)
	// as a JSON snapshot associated with the given migration index.
	CaptureSchemaSnapshot(ctx context.Context, migrationIndex string) error
	// GetSchemaSnapshot retrieves the stored schema JSON for a specific migration.
	GetSchemaSnapshot(ctx context.Context, migrationIndex string) (string, error)
	// GetLatestSnapshotIndex returns the migration index of the most recent snapshot.
	GetLatestSnapshotIndex(ctx context.Context) (string, error)
}
