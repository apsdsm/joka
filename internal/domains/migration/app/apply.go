package app

import (
	"context"
	"fmt"

	"github.com/nickfiggins/joka/internal/domains/migration/domain"
)

// ApplyAction encapsulates the dependencies needed to apply a single migration.
type ApplyAction struct {
	DB        DBAdapter
	Migration domain.Migration
}

// Execute performs the action of applying the migration to the database.
func (a ApplyAction) Execute(ctx context.Context) error {
	if err := a.DB.ApplySQLFromFile(ctx, a.Migration.FileFullPath); err != nil {
		return fmt.Errorf("applying migration %s: %w", a.Migration.MigrationIndex, err)
	}

	if err := a.DB.RecordMigrationApplied(ctx, a.Migration.MigrationIndex); err != nil {
		return fmt.Errorf("recording migration %s: %w", a.Migration.MigrationIndex, err)
	}

	return nil
}
