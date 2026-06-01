package app

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// SyncResult reports the outcome of an entity sync run: files whose entities
// were newly inserted, and files whose existing rows were updated in place.
type SyncResult struct {
	Synced  []string
	Updated []string
}

// SyncEntitiesAction is the orchestrator for entity sync. For each new file it
// inserts the entity graph; for each modified file it updates the existing rows
// in place (matched by _id) and inserts any genuinely-new entities. Files whose
// content is unchanged are skipped by the caller before reaching this action.
type SyncEntitiesAction struct {
	DB DBAdapter
	// Files are new files (not yet tracked) to insert.
	Files []*domain.EntityFile
	// Modified are tracked files whose content changed, to update in place.
	Modified []*domain.EntityFile
}

// Execute inserts new files and updates modified ones, returning which files
// were synced (inserted) and which were updated.
func (a SyncEntitiesAction) Execute(ctx context.Context) (*SyncResult, error) {
	result := &SyncResult{}

	for _, file := range a.Files {
		already, err := a.DB.IsEntitySynced(ctx, file.Path)
		if err != nil {
			return nil, err
		}

		if already {
			continue
		}

		if err := ValidateRefIDs(file.Entities); err != nil {
			return nil, err
		}

		refMap := make(map[string]int64)

		action := &InsertGraphAction{
			DB:         a.DB,
			Entities:   file.Entities,
			RefMap:     refMap,
			EntityFile: file.Path,
		}

		if err := action.Execute(ctx); err != nil {
			return nil, err
		}

		for _, row := range action.TrackedRows {
			if err := a.DB.RecordEntityRow(ctx, row); err != nil {
				return nil, err
			}
		}

		if file.ContentHash != "" {
			if err := a.DB.RecordEntitySyncedWithHash(ctx, file.Path, file.ContentHash); err != nil {
				return nil, err
			}
		} else {
			if err := a.DB.RecordEntitySynced(ctx, file.Path); err != nil {
				return nil, err
			}
		}

		result.Synced = append(result.Synced, file.Path)
	}

	for _, file := range a.Modified {
		if err := a.updateFile(ctx, file); err != nil {
			return nil, err
		}
		result.Updated = append(result.Updated, file.Path)
	}

	return result, nil
}

// updateFile propagates field-level changes from a modified file to its tracked
// rows. The file's entity graph is flattened depth-first (the same order rows
// were inserted and recorded in joka_entity_rows) and each entity is UPDATEd in
// place against the tracked row at the same position. If the file changed
// structurally — a different number of entities, or an entity's table no longer
// matches the tracked row at that position — it returns ErrStructuralChange so
// the caller can recommend 'entity reimport'.
func (a SyncEntitiesAction) updateFile(ctx context.Context, file *domain.EntityFile) error {
	tracked, err := a.DB.GetTrackedRows(ctx, file.Path)
	if err != nil {
		return err
	}

	seq, ordered, err := alignTrackedRows(file.Path, file.Entities, tracked)
	if err != nil {
		return err
	}

	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	refMap := make(map[string]int64)

	// Depth-first order guarantees an entity is updated before anything that
	// references it, so {{ ref.id }} resolves against refMap.
	for i, e := range seq {
		row := ordered[i]

		columns, err := resolveColumns(ctx, e.Columns, refMap, now, a.DB)
		if err != nil {
			return fmt.Errorf("resolving %s: %w", e.Table, err)
		}

		if err := a.DB.UpdateRow(ctx, e.Table, row.PKColumn, row.RowPK, columns); err != nil {
			return fmt.Errorf("updating %s: %w", e.Table, err)
		}

		if e.RefID != "" {
			refMap[e.RefID] = row.RowPK
		}
	}

	return a.DB.UpdateEntitySynced(ctx, file.Path, file.ContentHash)
}

// alignTrackedRows validates that a modified file still matches its tracked
// rows and returns the file's entities flattened depth-first alongside the
// tracked rows sorted into the same (insertion) order, so element i of each
// slice describes the same row. It returns ErrStructuralChange when the file
// gained or lost an entity, an entity's table no longer matches the tracked
// row at its position, or an _id disagrees with the tracked row at its
// position.
func alignTrackedRows(filePath string, entities []domain.Entity, tracked []domain.TrackedRow) ([]domain.Entity, []domain.TrackedRow, error) {
	if err := ValidateRefIDs(entities); err != nil {
		return nil, nil, err
	}

	// GetTrackedRows returns rows in reverse insertion order (for deletion);
	// sort ascending so positions line up with the file's depth-first order.
	ordered := make([]domain.TrackedRow, len(tracked))
	copy(ordered, tracked)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].InsertionOrder < ordered[j].InsertionOrder
	})

	seq := flattenEntities(entities, nil)

	if len(seq) != len(ordered) {
		return nil, nil, fmt.Errorf("%w: %s now defines %d entities but %d are tracked (an entity was added or removed)",
			domain.ErrStructuralChange, filePath, len(seq), len(ordered))
	}

	for i, e := range seq {
		row := ordered[i]
		if e.Table != row.TableName {
			return nil, nil, fmt.Errorf("%w: %s entity #%d targets table %q but the tracked row is in %q",
				domain.ErrStructuralChange, filePath, i+1, e.Table, row.TableName)
		}
		// When both carry an _id and they disagree, the graph was reordered or
		// renamed — positional matching would update the wrong row.
		if e.RefID != "" && row.RefID != "" && e.RefID != row.RefID {
			return nil, nil, fmt.Errorf("%w: %s entity #%d has _id %q but the tracked row at that position has _id %q",
				domain.ErrStructuralChange, filePath, i+1, e.RefID, row.RefID)
		}
	}

	return seq, ordered, nil
}

// flattenEntities returns the entity graph in depth-first pre-order (parent
// before its children, siblings in order) — the same order InsertGraphAction
// inserts rows and records their insertion_order.
func flattenEntities(entities []domain.Entity, out []domain.Entity) []domain.Entity {
	for _, e := range entities {
		out = append(out, e)
		out = flattenEntities(e.Children, out)
	}
	return out
}
