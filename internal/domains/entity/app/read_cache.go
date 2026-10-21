package app

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// BulkReader is the optional half of a DBAdapter that reads many rows in one
// round trip. An adapter without it is read a row at a time, as before.
type BulkReader interface {
	// GetRows is GetRow for several primary keys at once. A key with no row is
	// absent from the result.
	GetRows(ctx context.Context, table string, columns []string, pkColumn string, pks []int64) (map[int64]map[string]any, error)

	// RowsExist reports which of the primary keys are present.
	RowsExist(ctx context.Context, table, pkColumn string, pks []int64) (map[int64]bool, error)
}

// bulkChunk bounds how many keys one query carries.
const bulkChunk = 5000

type rowRef struct {
	table, pkColumn string
	pk              int64
}

// readCache is a DBAdapter for the planner. It answers GetRow, RowExists and
// LookupValue from what it has already read and falls through to the real
// adapter for everything else.
//
// The plan is read-only and reads each tracked row once, so over a connection
// with any latency its cost was one round trip per entity: 2,868 queries to
// find that 2,850 entities needed nothing. prefetch reads a table's rows in one
// query, and the per-entity reads that follow are answered from memory.
//
// It must not be given to the apply. The apply writes, and a cache of what the
// rows held before would answer for rows it has just changed.
type readCache struct {
	DBAdapter
	bulk BulkReader

	// rows holds the columns read for a row, and gone the rows known absent.
	rows map[rowRef]map[string]any
	gone map[rowRef]bool
	live map[rowRef]bool

	lookups map[string]any

	// failed is the first error a bulk read returned. Inside a transaction it also
	// poisons every statement after it, so the per-row read that follows would
	// report "current transaction is aborted" and bury the message that matters.
	failed error
}

func newReadCache(db DBAdapter) *readCache {
	bulk, _ := db.(BulkReader)

	return &readCache{
		DBAdapter: db,
		bulk:      bulk,
		rows:      make(map[rowRef]map[string]any),
		gone:      make(map[rowRef]bool),
		live:      make(map[rowRef]bool),
		lookups:   make(map[string]any),
	}
}

type tableRead struct {
	columns map[string]bool
	pks     []int64
}

// prefetch reads, one query per table, every tracked row the declared set will
// be compared against.
//
// A failed bulk read is not an error here. The per-row path that follows asks
// for the same rows and reports the failure with the row it concerns, which is
// a better message than one about a batch.
func (c *readCache) prefetch(ctx context.Context, state *domain.State, files []*domain.EntityFile) {
	if c.bulk == nil {
		return
	}

	type tableKey struct{ table, pkColumn string }
	reads := make(map[tableKey]*tableRead)

	for _, file := range files {
		for _, e := range flattenEntities(file.Entities, nil) {
			row, tracked := state.Row(e.RefID)
			if !tracked || row.Table != e.Table || len(e.Columns) == 0 {
				continue
			}

			key := tableKey{e.Table, pkColumnOf(row)}
			read := reads[key]
			if read == nil {
				read = &tableRead{columns: make(map[string]bool)}
				reads[key] = read
			}

			read.pks = append(read.pks, row.PKValue)
			for name := range e.Columns {
				read.columns[name] = true
			}
		}
	}

	for key, read := range reads {
		columns := make([]string, 0, len(read.columns))
		for name := range read.columns {
			columns = append(columns, name)
		}
		sort.Strings(columns)

		for _, pks := range chunks(read.pks) {
			found, err := c.bulk.GetRows(ctx, key.table, columns, key.pkColumn, pks)
			if err != nil {
				c.fail(err)
				continue
			}

			for _, pk := range pks {
				ref := rowRef{key.table, key.pkColumn, pk}
				if values, ok := found[pk]; ok {
					c.rows[ref] = values
				} else {
					c.gone[ref] = true
				}
			}
		}
	}
}

// prefetchLiveness reads, one query per table, whether each of the rows still
// exists.
func (c *readCache) prefetchLiveness(ctx context.Context, rows []domain.TrackedRow) {
	if c.bulk == nil {
		return
	}

	type tableKey struct{ table, pkColumn string }
	wanted := make(map[tableKey][]int64)

	for _, row := range rows {
		if row.RefID == "" {
			continue
		}
		key := tableKey{row.TableName, row.PKColumn}
		wanted[key] = append(wanted[key], row.RowPK)
	}

	for key, pks := range wanted {
		for _, batch := range chunks(pks) {
			present, err := c.bulk.RowsExist(ctx, key.table, key.pkColumn, batch)
			if err != nil {
				c.fail(err)
				continue
			}
			for _, pk := range batch {
				c.live[rowRef{key.table, key.pkColumn, pk}] = present[pk]
			}
		}
	}
}

// GetRow serves a row from memory when every column asked for was read, and
// otherwise reads it and remembers what it got.
func (c *readCache) GetRow(ctx context.Context, table string, columns []string, pkColumn string, pkValue int64) (map[string]any, error) {
	ref := rowRef{table, pkColumn, pkValue}

	if c.gone[ref] {
		return nil, fmt.Errorf("%w: %s %s=%d", domain.ErrRowNotFound, table, pkColumn, pkValue)
	}

	if have, ok := c.rows[ref]; ok {
		out := make(map[string]any, len(columns))
		complete := true
		for _, name := range columns {
			value, present := have[name]
			if !present {
				complete = false
				break
			}
			out[name] = value
		}
		if complete {
			return out, nil
		}
	}

	values, err := c.DBAdapter.GetRow(ctx, table, columns, pkColumn, pkValue)
	if err != nil {
		if c.failed != nil {
			return nil, c.failed
		}
		if errors.Is(err, domain.ErrRowNotFound) {
			c.gone[ref] = true
		}
		return nil, err
	}

	merged := c.rows[ref]
	if merged == nil {
		merged = make(map[string]any, len(values))
		c.rows[ref] = merged
	}
	for name, value := range values {
		merged[name] = value
	}

	return values, nil
}

// RowExists answers from the liveness prefetch, or from a row already read.
func (c *readCache) RowExists(ctx context.Context, table, pkColumn string, pkValue int64) (bool, error) {
	ref := rowRef{table, pkColumn, pkValue}

	if live, ok := c.live[ref]; ok {
		return live, nil
	}
	if c.gone[ref] {
		return false, nil
	}
	if _, ok := c.rows[ref]; ok {
		return true, nil
	}

	ok, err := c.DBAdapter.RowExists(ctx, table, pkColumn, pkValue)
	if err != nil && c.failed != nil {
		return false, c.failed
	}

	return ok, err
}

// LookupValue remembers a lookup that found its row. A miss is asked again each
// time: a lookup with no target is the uncommon case, and caching an absence
// would save little for the risk of reporting one that has since been filled.
func (c *readCache) LookupValue(ctx context.Context, table, returnCol, whereCol string, whereVal any) (any, error) {
	key := fmt.Sprintf("%s\x00%s\x00%s\x00%v", table, returnCol, whereCol, whereVal)

	if value, ok := c.lookups[key]; ok {
		return value, nil
	}

	value, err := c.DBAdapter.LookupValue(ctx, table, returnCol, whereCol, whereVal)
	if err != nil {
		return nil, err
	}

	c.lookups[key] = value
	return value, nil
}

// fail remembers the first bulk read error.
func (c *readCache) fail(err error) {
	if c.failed == nil {
		c.failed = err
	}
}

// pkColumnOf is the primary key column a tracked row is read by.
func pkColumnOf(row domain.EntityState) string {
	if row.PKColumn == "" {
		return "id"
	}
	return row.PKColumn
}

// chunks splits keys into batches of at most bulkChunk.
func chunks(pks []int64) [][]int64 {
	var out [][]int64
	for len(pks) > bulkChunk {
		out = append(out, pks[:bulkChunk])
		pks = pks[bulkChunk:]
	}
	if len(pks) > 0 {
		out = append(out, pks)
	}
	return out
}
