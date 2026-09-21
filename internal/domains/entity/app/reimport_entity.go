package app

import (
	"context"
	"fmt"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// ReimportEntityAction deletes the previously inserted rows for an entity file
// (in reverse insertion order) and re-inserts from the YAML definition.
type ReimportEntityAction struct {
	DB DBAdapter
	// Backend is loaded inside the caller's transaction and saved at the end of
	// it, so a rollback takes the tracking with the rows.
	Backend     StateBackend
	Secrets     SecretResolver
	FilePath    string // relative path (tracking key)
	FullPath    string // absolute path for re-parsing
	ContentHash string
}

// Execute performs the reimport. The caller is expected to wrap this in a
// transaction.
func (a ReimportEntityAction) Execute(ctx context.Context) error {
	state, err := a.Backend.Load(ctx)
	if err != nil {
		return err
	}

	if _, synced := state.FileHash(a.FilePath); !synced {
		return fmt.Errorf("%w: %s", domain.ErrEntityNotSynced, a.FilePath)
	}

	// RowsInFile is in the order the rows were written; deletion has to run the
	// other way so children go before their parents and a foreign key does not
	// stop it.
	tracked := state.RowsInFile(a.FilePath)

	for i := len(tracked) - 1; i >= 0; i-- {
		row := tracked[i]
		if err := a.DB.DeleteRow(ctx, row.TableName, row.PKColumn, row.RowPK); err != nil {
			return err
		}
		state.Forget(row.RefID)
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
		Secrets:    a.Secrets,
		Entities:   file.Entities,
		RefMap:     refMap,
		EntityFile: a.FilePath,
	}

	if err := action.Execute(ctx); err != nil {
		return err
	}

	for _, row := range action.TrackedRows {
		if row.RefID == "" {
			return fmt.Errorf("%w: cannot track %s row %d from %s",
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

	return a.Backend.Save(ctx, state)
}
