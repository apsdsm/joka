package app

import (
	"context"
	"fmt"
	"time"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// InsertGraphAction walks an entity graph depth-first, resolving template
// expressions, inserting each row, capturing auto-increment ids into a
// reference map, and recursing into children.
//
// When EntityFile is set, each inserted row is recorded in TrackedRows so the
// caller can persist them to joka_entity_rows for reimport support.
type InsertGraphAction struct {
	DB          DBAdapter
	Entities    []domain.Entity
	RefMap      map[string]int64
	EntityFile  string
	TrackedRows []domain.TrackedRow

	insertOrder int
}

// Execute inserts every entity in the graph. It populates RefMap as it goes
// so child entities can reference parent ids via {{ parent._id }}.
func (a *InsertGraphAction) Execute(ctx context.Context) error {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")

	for _, entity := range a.Entities {
		if err := a.insertEntity(ctx, entity, now); err != nil {
			return err
		}
	}

	return nil
}

// insertEntity resolves columns, inserts the row, stores the auto-increment id
// in RefMap (if _id was provided), and recurses into children.
func (a *InsertGraphAction) insertEntity(ctx context.Context, entity domain.Entity, now string) error {
	columns, err := resolveColumns(ctx, entity.Columns, a.RefMap, now, a.DB)
	if err != nil {
		return fmt.Errorf("resolving %s: %w", entity.Table, err)
	}

	id, err := a.DB.InsertRow(ctx, entity.Table, columns, entity.PKColumn)
	if err != nil {
		return fmt.Errorf("inserting into %s: %w", entity.Table, err)
	}

	if entity.RefID != "" {
		a.RefMap[entity.RefID] = id
	}

	if a.EntityFile != "" {
		a.TrackedRows = append(a.TrackedRows, domain.TrackedRow{
			EntityFile:     a.EntityFile,
			TableName:      entity.Table,
			RowPK:          id,
			PKColumn:       entity.PKColumn,
			RefID:          entity.RefID,
			InsertionOrder: a.insertOrder,
		})
		a.insertOrder++
	}

	for _, child := range entity.Children {
		if err := a.insertEntity(ctx, child, now); err != nil {
			return err
		}
	}

	return nil
}
