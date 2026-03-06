package app

import (
	"context"
	"fmt"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// ReimportEntityAction deletes the previously inserted rows for an entity file
// (in reverse insertion order) and re-inserts from the YAML definition.
type ReimportEntityAction struct {
	DB          DBAdapter
	FilePath    string // relative path (tracking key)
	FullPath    string // absolute path for re-parsing
	ContentHash string
}

// Execute performs the reimport. The caller is expected to wrap this in a
// transaction.
func (a ReimportEntityAction) Execute(ctx context.Context) error {
	synced, err := a.DB.IsEntitySynced(ctx, a.FilePath)
	if err != nil {
		return err
	}
	if !synced {
		return fmt.Errorf("%w: %s", domain.ErrEntityNotSynced, a.FilePath)
	}

	// Get tracked rows in reverse insertion order (children first).
	tracked, err := a.DB.GetTrackedRows(ctx, a.FilePath)
	if err != nil {
		return err
	}

	// Delete each row in reverse order.
	for _, row := range tracked {
		if err := a.DB.DeleteRow(ctx, row.TableName, row.PKColumn, row.RowPK); err != nil {
			return err
		}
	}

	// Clear row tracking entries.
	if err := a.DB.DeleteTrackedRows(ctx, a.FilePath); err != nil {
		return err
	}

	// Re-parse the YAML file.
	file, err := ParseEntityAction{Path: a.FullPath}.Execute()
	if err != nil {
		return err
	}

	if err := ValidateRefIDs(file.Entities); err != nil {
		return err
	}

	// Re-insert the entity graph.
	refMap := make(map[string]int64)
	action := &InsertGraphAction{
		DB:         a.DB,
		Entities:   file.Entities,
		RefMap:     refMap,
		EntityFile: a.FilePath,
	}

	if err := action.Execute(ctx); err != nil {
		return err
	}

	// Record the new tracked rows.
	for _, row := range action.TrackedRows {
		if err := a.DB.RecordEntityRow(ctx, row); err != nil {
			return err
		}
	}

	// Update the tracking record with new hash.
	if err := a.DB.UpdateEntitySynced(ctx, a.FilePath, a.ContentHash); err != nil {
		return err
	}

	return nil
}
