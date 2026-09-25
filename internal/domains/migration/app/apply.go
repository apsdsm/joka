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
	// SkipSnapshot leaves joka_snapshots alone. Set by a speculative pass that
	// is going to roll back, where the snapshot is both discarded and, over a
	// remote link, the most expensive thing in the run.
	SkipSnapshot bool
}

// Execute applies a single migration in three steps:
//  1. Run the SQL from the migration file against the database.
//  2. Record the migration as applied in joka_migrations.
//  3. Capture a schema snapshot into joka_snapshots so the full DB state
//     at this point in the migration chain is preserved, unless SkipSnapshot.
func (a ApplyAction) Execute(ctx context.Context) error {
	if err := a.DB.ApplySQLFromFile(ctx, a.Migration.FileFullPath); err != nil {
		return fmt.Errorf("applying migration %s: %w", a.Migration.MigrationIndex, err)
	}

	if err := a.DB.RecordMigrationApplied(ctx, a.Migration.MigrationIndex); err != nil {
		return fmt.Errorf("recording migration %s: %w", a.Migration.MigrationIndex, err)
	}

	// Skipped by a speculative pass, which is about to roll back: the snapshot
	// would be discarded with everything else, and it is the most expensive
	// thing here by a wide margin. ComputeSchema reconstructs every table from
	// pg_catalog with a query per table, so capturing after each of 29
	// migrations is hundreds of round trips - which over a tunnel to another
	// region is minutes, and `joka apply` was paying it twice.
	//
	// Nothing in a plan reads a snapshot. They feed `migrate verify` and the
	// drift line in `joka status`, both of which read what the real pass wrote.
	if a.SkipSnapshot {
		return nil
	}

	if err := a.DB.CaptureSchemaSnapshot(ctx, a.Migration.MigrationIndex); err != nil {
		return fmt.Errorf("capturing snapshot for migration %s: %w", a.Migration.MigrationIndex, err)
	}

	return nil
}
