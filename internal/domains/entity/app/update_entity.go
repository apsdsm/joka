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
	DB DBAdapter
	// Backend is loaded inside the caller's transaction and saved at the end of
	// it, so a rollback takes the tracking with the rows.
	Backend     StateBackend
	Secrets     SecretResolver
	FilePath    string // relative path (tracking key)
	FullPath    string // absolute path for parsing
	ContentHash string
}

// Execute performs the update. The caller is expected to wrap this in a
// transaction.
func (a UpdateEntityAction) Execute(ctx context.Context) (*UpdateEntityResult, error) {
	state, err := a.Backend.Load(ctx)
	if err != nil {
		return nil, err
	}

	if _, synced := state.FileHash(a.FilePath); !synced {
		return nil, fmt.Errorf("%w: %s", domain.ErrEntityNotSynced, a.FilePath)
	}

	tracked := state.RowsInFile(a.FilePath)

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
		Secrets:     a.Secrets,
		Entities:    file.Entities,
		RefMap:      refMap,
		EntityFile:  a.FilePath,
		SkipRefIDs:  skipRefIDs,
		insertOrder: maxOrder + 1,
	}

	if err := action.Execute(ctx); err != nil {
		return nil, err
	}

	for _, row := range action.TrackedRows {
		if row.RefID == "" {
			return nil, fmt.Errorf("%w: cannot track %s row %d from %s",
				domain.ErrEntitySetInvalid, row.TableName, row.RowPK, a.FilePath)
		}
		state.Track(row.RefID, domain.EntityState{
			Table:    row.TableName,
			PKColumn: row.PKColumn,
			PKValue:  row.RowPK,
			File:     a.FilePath,
			Order:    row.InsertionOrder,
			Columns:  action.Baselines[row.RefID],
		})
	}

	state.TrackFile(a.FilePath, a.ContentHash)

	if err := a.Backend.Save(ctx, state); err != nil {
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
