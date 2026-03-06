package app

import (
	"context"
	"path/filepath"
	"sort"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// EntityStatusAction compares entity files on disk with the joka_entities
// tracking table to determine which files are synced, modified, new, or
// orphaned.
type EntityStatusAction struct {
	DB          DBAdapter
	EntitiesDir string
	Files       []string // relative paths from DiscoverEntityFiles
}

// Execute returns the status of all entity files.
func (a EntityStatusAction) Execute(ctx context.Context) ([]domain.EntityFileInfo, error) {
	synced, err := a.DB.GetAllSyncedEntities(ctx)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var result []domain.EntityFileInfo

	for _, rel := range a.Files {
		seen[rel] = true
		fullPath := filepath.Join(a.EntitiesDir, rel)

		hash, err := HashFileContent(fullPath)
		if err != nil {
			return nil, err
		}

		dbHash, tracked := synced[rel]
		if !tracked {
			result = append(result, domain.EntityFileInfo{Path: rel, Status: domain.StatusNew})
			continue
		}

		if dbHash == "" || dbHash != hash {
			result = append(result, domain.EntityFileInfo{Path: rel, Status: domain.StatusModified})
		} else {
			result = append(result, domain.EntityFileInfo{Path: rel, Status: domain.StatusSynced})
		}
	}

	for file := range synced {
		if !seen[file] {
			result = append(result, domain.EntityFileInfo{Path: file, Status: domain.StatusOrphaned})
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Path < result[j].Path
	})

	return result, nil
}
