package app

import (
	"context"

	"github.com/nickfiggins/joka/internal/domains/migration/infra/models"
)

// DBAdapter defines the methods required to interact with the database for
// migration operations.
type DBAdapter interface {
	HasMigrationsTable(ctx context.Context) (bool, error)
	CreateMigrationsTable(ctx context.Context) error
	GetAppliedMigrations(ctx context.Context) ([]models.MigrationRow, error)
	ApplySQLFromFile(ctx context.Context, filePath string) error
	RecordMigrationApplied(ctx context.Context, migrationIndex string) error
}
