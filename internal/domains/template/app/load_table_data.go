package app

import (
	"context"

	"github.com/nickfiggins/joka/internal/domains/template/domain"
	"github.com/nickfiggins/joka/internal/domains/template/infra"
)

type LoadTableDataAction struct {
	Table domain.Table
}

func (a LoadTableDataAction) Execute(ctx context.Context) ([]map[string]any, error) {
	var allRows []map[string]any
	for _, record := range a.Table.Records {
		rows, err := infra.LoadRecord(record)
		if err != nil {
			return nil, err
		}
		allRows = append(allRows, rows...)
	}
	return allRows, nil
}
