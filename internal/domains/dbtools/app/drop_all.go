package app

import "context"

// DropAllAction drops every user table in the current database/schema,
// including joka_* tracking tables. Used by `joka drop` and `joka reset`.
type DropAllAction struct {
	DB DBAdapter
}

// Execute returns the list of tables that were dropped.
func (a DropAllAction) Execute(ctx context.Context) ([]string, error) {
	tables, err := a.DB.ListTables(ctx)
	if err != nil {
		return nil, err
	}
	if len(tables) == 0 {
		return nil, nil
	}
	if err := a.DB.DropAllTables(ctx); err != nil {
		return nil, err
	}
	return tables, nil
}
