package app

import (
	"context"

	"github.com/apsdsm/joka/internal/domains/template/domain"
)

type SyncTableAction struct {
	DB    DBAdapter
	Table domain.Table
}

func (a SyncTableAction) Execute(ctx context.Context) (int, error) {
	rows, err := LoadTableDataAction{Table: a.Table}.Execute(ctx)
	if err != nil {
		return 0, err
	}

	if err := a.DB.TruncateTable(ctx, a.Table.Name); err != nil {
		return 0, err
	}

	if len(rows) == 0 {
		return 0, nil
	}

	return a.DB.InsertRows(ctx, a.Table.Name, rows)
}
