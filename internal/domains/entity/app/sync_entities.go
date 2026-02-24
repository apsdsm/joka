package app

import (
	"context"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// SyncEntitiesAction is the orchestrator for entity sync. For each file it
// checks whether it was already synced (via the joka_entities tracking table),
// inserts new entity graphs, and records the file as synced.
type SyncEntitiesAction struct {
	DB    DBAdapter
	Files []*domain.EntityFile
}

// Execute processes each entity file. Files that have already been synced are
// skipped. Returns the list of file paths that were newly synced.
func (a SyncEntitiesAction) Execute(ctx context.Context) ([]string, error) {
	var synced []string

	for _, file := range a.Files {
		already, err := a.DB.IsEntitySynced(ctx, file.Path)
		if err != nil {
			return nil, err
		}

		if already {
			continue
		}

		refMap := make(map[string]int64)

		err = InsertGraphAction{
			DB:       a.DB,
			Entities: file.Entities,
			RefMap:   refMap,
		}.Execute(ctx)
		if err != nil {
			return nil, err
		}

		if err := a.DB.RecordEntitySynced(ctx, file.Path); err != nil {
			return nil, err
		}

		synced = append(synced, file.Path)
	}

	return synced, nil
}
