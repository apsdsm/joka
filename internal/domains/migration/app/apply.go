package app

import (
	"context"
	"fmt"

	"github.com/apsdsm/joka/internal/domains/migration/domain"
)

// ApplyAction encapsulates the dependencies needed to apply a single migration.
type ApplyAction struct {
	DB        DBAdapter
	Migration domain.Migration
}

// Execute applies a single migration in three steps:
//  1. Run the SQL from the migration file against the database.
//  2. Record the migration as applied in joka_migrations.
//  3. Capture a schema snapshot into joka_snapshots so the full DB state
//     at this point in the migration chain is preserved.
func (a ApplyAction) Execute(ctx context.Context) error {
	if err := a.DB.ApplySQLFromFile(ctx, a.Migration.FileFullPath); err != nil {
		return fmt.Errorf("applying migration %s: %w", a.Migration.MigrationIndex, err)
	}

	if err := a.DB.RecordMigrationApplied(ctx, a.Migration.MigrationIndex); err != nil {
		return fmt.Errorf("recording migration %s: %w", a.Migration.MigrationIndex, err)
	}

	if err := a.DB.CaptureSchemaSnapshot(ctx, a.Migration.MigrationIndex); err != nil {
		return fmt.Errorf("capturing snapshot for migration %s: %w", a.Migration.MigrationIndex, err)
	}

	return nil
}
