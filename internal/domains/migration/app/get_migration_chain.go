package app

import (
	"context"
	"fmt"

	"github.com/nickfiggins/joka/internal/domains/migration/domain"
	"github.com/nickfiggins/joka/internal/domains/migration/infra"
)

// GetMigrationChainAction encapsulates the dependencies needed to build the
// full migration chain by combining files on disk with applied migrations in the
// database.
type GetMigrationChainAction struct {
	DB            DBAdapter
	MigrationsDir string
}

// Execute performs the action of retrieving the full migration chain. It reads
// migration files from disk and applied migrations from the database, then
// combines them into a single ordered list with computed statuses.
func (a GetMigrationChainAction) Execute(ctx context.Context) ([]domain.Migration, error) {
	files, err := infra.ListMigrationFiles(a.MigrationsDir)
	if err != nil {
		return nil, err
	}

	applied, err := a.DB.GetAppliedMigrations(ctx)
	if err != nil {
		return nil, err
	}

	var migrations []domain.Migration
	idx := 0
	lenFiles := len(files)
	lenApplied := len(applied)

	for {
		if idx >= lenFiles && idx >= lenApplied {
			break
		}

		// File exists but no applied migration at this index -> pending
		if idx < lenFiles && idx >= lenApplied {
			migrations = append(migrations, domain.Migration{
				ID:             idx,
				MigrationIndex: files[idx].Index,
				AppliedAt:      "",
				FileName:       files[idx].Name,
				FileFullPath:   files[idx].FullPath,
				Status:         domain.StatusPending,
			})
			idx++
			continue
		}

		// Both file and applied migration exist at this index
		if idx < lenFiles && idx < lenApplied {
			file := files[idx]
			row := applied[idx]

			if file.Index != row.MigrationIndex {
				return nil, fmt.Errorf("migration chain broken at index %d: wanted %s but found %s", idx, file.Index, row.MigrationIndex)
			}

			migrations = append(migrations, domain.Migration{
				ID:             idx,
				MigrationIndex: file.Index,
				AppliedAt:      row.AppliedAt.Format("2006-01-02 15:04:05"),
				FileName:       file.Name,
				FileFullPath:   file.FullPath,
				Status:         domain.StatusApplied,
			})
			idx++
			continue
		}

		// Applied migration exists but no file -> chain broken
		if idx >= lenFiles && idx < lenApplied {
			return nil, fmt.Errorf("migration file missing for applied migration at index %d: %s", idx, applied[idx].MigrationIndex)
		}
	}

	return migrations, nil
}
