package app

import (
	"path/filepath"
	"sort"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// EntityStatusAction compares the entity files on disk with what joka last
// applied, to say which are synced, modified, new or orphaned.
//
// It reads no database of its own: the state is loaded once by the caller. That
// is what lets `entity forget --orphans` resolve its targets through this
// action rather than reimplementing the comparison, and it makes the whole
// thing testable from a literal.
type EntityStatusAction struct {
	State       *domain.State
	EntitiesDir string
	Files       []string // relative paths from DiscoverEntityFiles
}

// Execute returns the status of all entity files.
func (a EntityStatusAction) Execute() ([]domain.EntityFileInfo, error) {
	seen := make(map[string]bool)
	var result []domain.EntityFileInfo

	for _, rel := range a.Files {
		seen[rel] = true
		fullPath := filepath.Join(a.EntitiesDir, rel)

		hash, err := HashFileContent(fullPath)
		if err != nil {
			return nil, err
		}

		stored, tracked := a.State.FileHash(rel)

		result = append(result, domain.EntityFileInfo{
			Path:   rel,
			Status: FileStatusFor(tracked, stored, hash),
		})
	}

	// Tracked with nothing on disk. Nothing but this comparison reports them,
	// and `entity forget --orphans` is what clears them.
	for path := range a.State.Files {
		if !seen[path] {
			result = append(result, domain.EntityFileInfo{Path: path, Status: domain.StatusOrphaned})
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Path < result[j].Path
	})

	return result, nil
}
