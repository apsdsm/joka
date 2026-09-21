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
	// Prune deletes rows the file no longer declares, as well as the ones it
	// does. Without it they are left alone and reported in Undeclared.
	Prune bool
}

// Execute performs the reimport. The caller is expected to wrap this in a
// transaction. It returns the tracked rows it left alone because the file
// stopped declaring them.
func (a ReimportEntityAction) Execute(ctx context.Context) ([]domain.TrackedRow, error) {
	var undeclared []domain.TrackedRow

	state, err := a.Backend.Load(ctx)
	if err != nil {
		return nil, err
	}

	if _, synced := state.FileHash(a.FilePath); !synced {
		return nil, fmt.Errorf("%w: %s", domain.ErrEntityNotSynced, a.FilePath)
	}

	// Re-parse the YAML file.
	file, err := ParseEntityAction{Path: a.FullPath}.Execute()
	if err != nil {
		return undeclared, err
	}

	declared := make(map[string]bool)
	for _, e := range flattenEntities(file.Entities, nil) {
		declared[e.RefID] = true
	}

	// RowsInFile is in the order the rows were written; deletion has to run the
	// other way so children go before their parents and a foreign key does not
	// stop it.
	tracked := state.RowsInFile(a.FilePath)

	for i := len(tracked) - 1; i >= 0; i-- {
		row := tracked[i]

		// A row the file no longer declares is not this reimport's to delete.
		// `entity sync` refuses to delete an undeclared entity on the grounds
		// that a seed file edited by mistake must not take data with it, and
		// reimport deleting it silently — while the output said only "tracked
		// rows to delete: N" — was the same mistake with a different command
		// name on it. Prune says to mean it.
		if !declared[row.RefID] && !a.Prune {
			undeclared = append(undeclared, row)
			continue
		}

		if err := a.DB.DeleteRow(ctx, row.TableName, row.PKColumn, row.RowPK); err != nil {
			return undeclared, err
		}
		state.Forget(row.RefID)
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
		return undeclared, err
	}

	for _, row := range action.TrackedRows {
		if row.RefID == "" {
			return undeclared, fmt.Errorf("%w: cannot track %s row %d from %s",
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

	return undeclared, a.Backend.Save(ctx, state)
}
