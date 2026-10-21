package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// bulkMock is the mock adapter with the optional bulk reads, counting every
// read so a test can say how many round trips a plan took.
type bulkMock struct {
	*mockDBAdapter

	getRow, getRows, rowExists, rowsExist, lookups int
}

func (b *bulkMock) GetRow(ctx context.Context, table string, columns []string, pkColumn string, pk int64) (map[string]any, error) {
	b.getRow++
	return b.mockDBAdapter.GetRow(ctx, table, columns, pkColumn, pk)
}

func (b *bulkMock) RowExists(ctx context.Context, table, pkColumn string, pk int64) (bool, error) {
	b.rowExists++
	return b.mockDBAdapter.RowExists(ctx, table, pkColumn, pk)
}

func (b *bulkMock) LookupValue(ctx context.Context, table, returnCol, whereCol string, whereVal any) (any, error) {
	b.lookups++
	return b.mockDBAdapter.LookupValue(ctx, table, returnCol, whereCol, whereVal)
}

func (b *bulkMock) GetRows(_ context.Context, table string, columns []string, _ string, pks []int64) (map[int64]map[string]any, error) {
	b.getRows++

	out := make(map[int64]map[string]any)
	for _, pk := range pks {
		row, ok := b.currentRows[fmt.Sprintf("%s|%d", table, pk)]
		if !ok {
			continue
		}
		values := make(map[string]any, len(columns))
		for _, c := range columns {
			values[c] = row[c]
		}
		out[pk] = values
	}
	return out, nil
}

func (b *bulkMock) RowsExist(_ context.Context, table, _ string, pks []int64) (map[int64]bool, error) {
	b.rowsExist++

	out := make(map[int64]bool)
	for _, pk := range pks {
		if _, ok := b.currentRows[fmt.Sprintf("%s|%d", table, pk)]; ok {
			out[pk] = true
		}
	}
	return out, nil
}

// trackedSet builds n tracked entities in one table, all in the database, and
// the file declaring them. Entity 0's email differs from what the database holds.
func trackedSet(n int) (*bulkMock, []*domain.EntityFile) {
	db := &bulkMock{mockDBAdapter: newMockDBAdapter()}

	var rows []domain.TrackedRow
	var entities []domain.Entity

	for i := 0; i < n; i++ {
		id := fmt.Sprintf("u%d", i)
		pk := int64(i + 1)

		rows = append(rows, domain.TrackedRow{TableName: "users", RowPK: pk, PKColumn: "id", RefID: id, InsertionOrder: i})
		db.currentRows[fmt.Sprintf("users|%d", pk)] = map[string]any{"email": id + "@example.com"}

		email := id + "@example.com"
		if i == 0 {
			email = "changed@example.com"
		}
		entities = append(entities, domain.Entity{
			Table: "users", RefID: id, PKColumn: "id", Columns: map[string]any{"email": email},
		})
	}

	db.track("users.yaml", rows...)

	return db, []*domain.EntityFile{{Path: "users.yaml", Entities: entities}}
}

func TestPlanReadsInBulk(t *testing.T) {
	t.Run("it reads a table's tracked rows in one query", func(t *testing.T) {
		db, files := trackedSet(40)

		plan, err := (PlanSyncAction{DB: db, State: db.state, Declared: files, Dirty: dirtySet(files)}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if db.getRows != 1 {
			t.Errorf("expected one bulk read, got %d", db.getRows)
		}
		if db.getRow != 0 {
			t.Errorf("expected no single-row reads, got %d", db.getRow)
		}

		if len(plan.Updates) != 1 || len(plan.Updates[0].Rows) != 1 || plan.Updates[0].Rows[0].RefID != "u0" {
			t.Fatalf("expected the one changed row planned, got %+v", plan.Updates)
		}
	})

	t.Run("it plans the same as an adapter that cannot read in bulk", func(t *testing.T) {
		bulk, files := trackedSet(10)
		single, _ := trackedSet(10)

		got, err := (PlanSyncAction{DB: bulk, State: bulk.state, Declared: files, Dirty: dirtySet(files)}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want, err := (PlanSyncAction{DB: single.mockDBAdapter, State: single.state, Declared: files, Dirty: dirtySet(files)}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if fmt.Sprintf("%+v", got) != fmt.Sprintf("%+v", want) {
			t.Errorf("plans differ:\n bulk:   %+v\n single: %+v", got, want)
		}
	})

	t.Run("it still plans a recreate for a tracked row that is gone", func(t *testing.T) {
		db, files := trackedSet(5)
		delete(db.currentRows, "users|3")

		plan, err := (PlanSyncAction{DB: db, State: db.state, Declared: files, Dirty: dirtySet(files)}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !plan.Recreate["u2"] {
			t.Errorf("expected u2 to be recreated, got %v", plan.Recreate)
		}
	})

	t.Run("it checks the rows nothing declares in one query", func(t *testing.T) {
		db, files := trackedSet(5)

		var orphans []domain.TrackedRow
		for i := 0; i < 20; i++ {
			pk := int64(100 + i)
			orphans = append(orphans, domain.TrackedRow{TableName: "users", RowPK: pk, PKColumn: "id", RefID: fmt.Sprintf("gone%d", i)})
			if i%2 == 0 {
				db.currentRows[fmt.Sprintf("users|%d", pk)] = map[string]any{}
			}
		}
		db.track("old.yaml", orphans...)

		plan, err := (PlanSyncAction{DB: db, State: db.state, Declared: files, Dirty: dirtySet(files)}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if db.rowExists != 0 || db.rowsExist != 1 {
			t.Errorf("expected one bulk check and no single ones, got %d and %d", db.rowsExist, db.rowExists)
		}
		if len(plan.Deletes) != 10 || len(plan.Forgets) != 10 {
			t.Errorf("expected 10 deletes and 10 forgets, got %d and %d", len(plan.Deletes), len(plan.Forgets))
		}
	})

	t.Run("it resolves a repeated lookup once", func(t *testing.T) {
		db, _ := trackedSet(0)
		db.lookupData["types.id.code=A"] = int64(7)

		var rows []domain.TrackedRow
		var entities []domain.Entity
		for i := 0; i < 10; i++ {
			pk := int64(i + 1)
			id := fmt.Sprintf("w%d", i)
			rows = append(rows, domain.TrackedRow{TableName: "widgets", RowPK: pk, PKColumn: "id", RefID: id, InsertionOrder: i})
			db.currentRows[fmt.Sprintf("widgets|%d", pk)] = map[string]any{"type_id": int64(7)}
			entities = append(entities, domain.Entity{
				Table: "widgets", RefID: id, PKColumn: "id",
				Columns: map[string]any{"type_id": "{{ lookup|types,id,code=A }}"},
			})
		}
		db.track("w.yaml", rows...)
		files := []*domain.EntityFile{{Path: "w.yaml", Entities: entities}}

		plan, err := (PlanSyncAction{DB: db, State: db.state, Declared: files, Dirty: dirtySet(files)}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if db.lookups != 1 {
			t.Errorf("expected one lookup, got %d", db.lookups)
		}
		if plan.HasChanges() {
			t.Errorf("expected nothing to change, got %+v", plan)
		}
	})

	t.Run("it does not cache a lookup that found nothing", func(t *testing.T) {
		db, _ := trackedSet(0)
		cache := newReadCache(db)

		_, err := cache.LookupValue(context.Background(), "types", "id", "code", "A")
		if !errors.Is(err, domain.ErrLookupNotFound) {
			t.Fatalf("expected a lookup miss, got %v", err)
		}

		db.lookupData["types.id.code=A"] = int64(7)

		got, err := cache.LookupValue(context.Background(), "types", "id", "code", "A")
		if err != nil || got != int64(7) {
			t.Errorf("expected the row once it exists, got %v, %v", got, err)
		}
	})
}

func TestApplyResolvesOnlyWhatItWrites(t *testing.T) {
	t.Run("it does not look up a column it will not write", func(t *testing.T) {
		db := &bulkMock{mockDBAdapter: newMockDBAdapter()}
		db.lookupData["types.id.code=A"] = int64(7)
		db.track("w.yaml", domain.TrackedRow{TableName: "widgets", RowPK: 1, PKColumn: "id", RefID: "w1"})
		db.currentRows["widgets|1"] = map[string]any{"name": "old", "type_id": int64(7)}

		files := []*domain.EntityFile{{Path: "w.yaml", Entities: []domain.Entity{{
			Table: "widgets", RefID: "w1", PKColumn: "id",
			Columns: map[string]any{"name": "new", "type_id": "{{ lookup|types,id,code=A }}"},
		}}}}

		_, err := ApplySetAction{
			DB: db, Backend: db.backend(), Declared: files, Dirty: dirtySet(files),
			Write: map[string][]string{"w1": {"name"}},
		}.Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if db.lookups != 0 {
			t.Errorf("expected the unwritten lookup left alone, got %d", db.lookups)
		}
		if len(db.updatedRows) != 1 || db.updatedRows[0].Columns["name"] != "new" {
			t.Errorf("expected name written, got %+v", db.updatedRows)
		}
	})
}

func TestApplyHashesInParallel(t *testing.T) {
	t.Run("it gives every row its own hash", func(t *testing.T) {
		db := newMockDBAdapter()

		var entities []domain.Entity
		for i := 0; i < 6; i++ {
			entities = append(entities, domain.Entity{
				Table: "users", RefID: fmt.Sprintf("u%d", i), PKColumn: "id",
				Columns: map[string]any{"password_hash": "{{ argon2id|secret }}"},
			})
		}
		files := []*domain.EntityFile{{Path: "u.yaml", Entities: entities}}

		_, err := ApplySetAction{DB: db, Backend: db.backend(), Declared: files, Dirty: dirtySet(files)}.Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		seen := make(map[string]bool)
		for _, call := range db.insertedRows {
			hash, _ := call.Columns["password_hash"].(string)
			if !strings.HasPrefix(hash, "$argon2id$") {
				t.Fatalf("expected an argon2id hash, got %q", hash)
			}
			if seen[hash] {
				t.Errorf("two rows share the hash %q; each needs its own salt", hash)
			}
			seen[hash] = true
		}
		if len(seen) != 6 {
			t.Errorf("expected 6 hashed rows, got %d", len(seen))
		}
	})
}

// failingBulk fails its bulk read and every read after it, the way a statement
// after an error inside a transaction does.
type failingBulk struct {
	*bulkMock
	aborted bool
}

func (f *failingBulk) GetRows(context.Context, string, []string, string, []int64) (map[int64]map[string]any, error) {
	f.aborted = true
	return nil, errors.New(`column "nope" does not exist`)
}

func (f *failingBulk) GetRow(ctx context.Context, table string, columns []string, pkColumn string, pk int64) (map[string]any, error) {
	if f.aborted {
		return nil, errors.New("current transaction is aborted")
	}
	return f.bulkMock.GetRow(ctx, table, columns, pkColumn, pk)
}

func TestPlanKeepsTheBulkReadError(t *testing.T) {
	t.Run("it reports the failed bulk read rather than the aborted transaction after it", func(t *testing.T) {
		bulk, files := trackedSet(3)
		db := &failingBulk{bulkMock: bulk}

		_, err := (PlanSyncAction{DB: db, State: bulk.state, Declared: files, Dirty: dirtySet(files)}).Execute(context.Background())
		if err == nil || !strings.Contains(err.Error(), `column "nope" does not exist`) {
			t.Errorf("expected the original error, got %v", err)
		}
	})
}
