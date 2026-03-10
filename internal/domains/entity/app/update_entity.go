package app

import (
	"context"
	"fmt"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// UpdateEntityResult holds the outcome of an entity update for reporting.
type UpdateEntityResult struct {
	Skipped  []UpdateEntityEntry
	Inserted []UpdateEntityEntry
}

// UpdateEntityEntry describes a single entity that was skipped or inserted.
type UpdateEntityEntry struct {
	Table string
	RefID string
	PK    int64 // populated for skipped entries
}

// UpdateEntityAction adds new entities from a YAML file without deleting
// existing tracked rows. Entities whose _id is already tracked are skipped;
// their PKs are loaded into the reference map so new children can reference
// them via {{ parent.id }}.
type UpdateEntityAction struct {
	DB          DBAdapter
	FilePath    string // relative path (tracking key)
	FullPath    string // absolute path for parsing
	ContentHash string
}

// Execute performs the update. The caller is expected to wrap this in a
// transaction.
func (a UpdateEntityAction) Execute(ctx context.Context) (*UpdateEntityResult, error) {
	synced, err := a.DB.IsEntitySynced(ctx, a.FilePath)
	if err != nil {
		return nil, err
	}
	if !synced {
		return nil, fmt.Errorf("%w: %s", domain.ErrEntityNotSynced, a.FilePath)
	}

	// Get tracked rows and build skip map (ref_id -> existing PK).
	tracked, err := a.DB.GetTrackedRows(ctx, a.FilePath)
	if err != nil {
		return nil, err
	}

	skipRefIDs := make(map[string]int64)
	for _, row := range tracked {
		if row.RefID != "" {
			skipRefIDs[row.RefID] = row.RowPK
		}
	}

	// Parse the YAML file.
	file, err := ParseEntityAction{Path: a.FullPath}.Execute()
	if err != nil {
		return nil, err
	}

	if err := ValidateRefIDs(file.Entities); err != nil {
		return nil, err
	}

	// Require _id on every entity so we can determine skip/insert.
	if err := validateAllHaveRefID(file.Entities); err != nil {
		return nil, err
	}

	// Insert the graph with skip support.
	refMap := make(map[string]int64)

	// Determine the next insertion_order based on existing tracked rows.
	maxOrder := -1
	for _, row := range tracked {
		if row.InsertionOrder > maxOrder {
			maxOrder = row.InsertionOrder
		}
	}

	action := &InsertGraphAction{
		DB:          a.DB,
		Entities:    file.Entities,
		RefMap:      refMap,
		EntityFile:  a.FilePath,
		SkipRefIDs:  skipRefIDs,
		insertOrder: maxOrder + 1,
	}

	if err := action.Execute(ctx); err != nil {
		return nil, err
	}

	// Record new tracked rows.
	for _, row := range action.TrackedRows {
		if err := a.DB.RecordEntityRow(ctx, row); err != nil {
			return nil, err
		}
	}

	// Update the content hash.
	if err := a.DB.UpdateEntitySynced(ctx, a.FilePath, a.ContentHash); err != nil {
		return nil, err
	}

	// Build result.
	result := &UpdateEntityResult{}
	for refID, pk := range skipRefIDs {
		// Find the table name from tracked rows.
		table := ""
		for _, row := range tracked {
			if row.RefID == refID {
				table = row.TableName
				break
			}
		}
		result.Skipped = append(result.Skipped, UpdateEntityEntry{
			Table: table,
			RefID: refID,
			PK:    pk,
		})
	}
	for _, row := range action.TrackedRows {
		result.Inserted = append(result.Inserted, UpdateEntityEntry{
			Table: row.TableName,
			RefID: row.RefID,
			PK:    row.RowPK,
		})
	}

	return result, nil
}

// validateAllHaveRefID checks that every entity in the tree has a non-empty
// _id. This is required for entity update so we can determine whether each
// entity is already tracked or new.
func validateAllHaveRefID(entities []domain.Entity) error {
	for _, e := range entities {
		if e.RefID == "" {
			return fmt.Errorf("%w: table %q", domain.ErrEntityMissingRefID, e.Table)
		}
		if err := validateAllHaveRefID(e.Children); err != nil {
			return err
		}
	}
	return nil
}
