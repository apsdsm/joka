package app

import (
	"context"
	"fmt"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// mockDBAdapter is a hand-rolled mock for the DBAdapter interface.
type mockDBAdapter struct {
	insertedRows  []mockInsertCall
	nextID        int64
	lookupData    map[string]any // keyed by "table.returnCol.whereCol=whereVal"
	deletedRows   []mockDeleteCall
	updatedRows   []mockUpdateCall
	currentRows   map[string]map[string]any // key: "table|pkValue" -> column values
	missingTables map[string]bool           // tables the mock reports as dropped

	// state is the tracking, and the only record of it: the adapter carries no
	// tracking methods any more. A fixture sets it up with track, a test reads
	// it back with fileHash or trackedRows.
	state *domain.State
}

// mockInsertCall records the arguments passed to InsertRow.
type mockInsertCall struct {
	Table   string
	Columns map[string]any
}

type mockDeleteCall struct {
	Table    string
	PKColumn string
	PKValue  int64
}

// mockUpdateCall records the arguments passed to UpdateRow.
type mockUpdateCall struct {
	Table    string
	PKColumn string
	PKValue  int64
	Columns  map[string]any
}

func newMockDBAdapter() *mockDBAdapter {
	return &mockDBAdapter{
		nextID:      1,
		lookupData:  make(map[string]any),
		currentRows: make(map[string]map[string]any),
		state:       domain.NewState(),
	}
}

// backend hands the actions the mock's own state, so an action that loads,
// mutates and saves round-trips through the fixture the test set up and the
// test can assert on it afterwards.
func (m *mockDBAdapter) backend() StateBackend { return mockStateBackend{m: m} }

type mockStateBackend struct{ m *mockDBAdapter }

func (b mockStateBackend) Load(context.Context) (*domain.State, error) { return b.m.state, nil }

func (b mockStateBackend) Save(_ context.Context, s *domain.State) error {
	b.m.state = s
	return nil
}

// track records a synced file and the rows tracked against it, under a fixed
// hash. It says nothing about whether those rows are still in the database.
func (m *mockDBAdapter) track(path string, rows ...domain.TrackedRow) {
	m.trackHash(path, "hash", rows...)
}

// trackHash is track for a test that cares what hash was recorded.
func (m *mockDBAdapter) trackHash(path, hash string, rows ...domain.TrackedRow) {
	m.state.TrackFile(path, hash)

	for _, r := range rows {
		r.EntityFile = path

		if r.RefID == "" {
			m.state.Unkeyed = append(m.state.Unkeyed, r)
			continue
		}

		m.state.Track(r.RefID, domain.EntityState{
			Table:    r.TableName,
			PKColumn: r.PKColumn,
			PKValue:  r.RowPK,
			File:     path,
			Order:    r.InsertionOrder,
		})
	}
}

// fileHash reports the hash recorded for a file and whether it is tracked.
func (m *mockDBAdapter) fileHash(path string) (string, bool) { return m.state.FileHash(path) }

// isTracked reports whether a file has a tracking record.
func (m *mockDBAdapter) isTracked(path string) bool {
	_, ok := m.state.FileHash(path)
	return ok
}

// trackedRows is every row the mock tracks, in a stable order.
func (m *mockDBAdapter) trackedRows() []domain.TrackedRow { return m.state.AllRows() }

func (m *mockDBAdapter) DeleteRow(_ context.Context, table, pkColumn string, pkValue int64) error {
	m.deletedRows = append(m.deletedRows, mockDeleteCall{Table: table, PKColumn: pkColumn, PKValue: pkValue})
	return nil
}

// TableExists reports every table as present unless the test marks it missing.
func (m *mockDBAdapter) TableExists(_ context.Context, table string) (bool, error) {
	return !m.missingTables[table], nil
}

// RowExists reports a row live when currentRows holds it. Tests that care
// about liveness seed currentRows; everything else reads as "already gone",
// which is the state entity forget is normally used on.
func (m *mockDBAdapter) RowExists(_ context.Context, table, _ string, pkValue int64) (bool, error) {
	_, ok := m.currentRows[fmt.Sprintf("%s|%d", table, pkValue)]
	return ok, nil
}

// InsertRow records the call and makes the row live, so a test that goes on to
// compare against the database sees what the insert put there.
func (m *mockDBAdapter) InsertRow(_ context.Context, table string, columns map[string]any, _ string) (int64, error) {
	m.insertedRows = append(m.insertedRows, mockInsertCall{Table: table, Columns: columns})

	id := m.nextID
	m.nextID++

	live := make(map[string]any, len(columns))
	for k, v := range columns {
		live[k] = v
	}
	m.currentRows[fmt.Sprintf("%s|%d", table, id)] = live

	return id, nil
}

// UpdateRow records the call and applies it to the live row. Without this the
// mock would leave the database disagreeing with the baseline joka just
// recorded, and every three-way comparison after an apply would read as drift.
func (m *mockDBAdapter) UpdateRow(_ context.Context, table, pkColumn string, pkValue int64, columns map[string]any) error {
	m.updatedRows = append(m.updatedRows, mockUpdateCall{Table: table, PKColumn: pkColumn, PKValue: pkValue, Columns: columns})

	key := fmt.Sprintf("%s|%d", table, pkValue)
	live, ok := m.currentRows[key]
	if !ok {
		live = make(map[string]any, len(columns))
		m.currentRows[key] = live
	}
	for k, v := range columns {
		live[k] = v
	}

	return nil
}

func (m *mockDBAdapter) GetRow(_ context.Context, table string, columns []string, _ string, pkValue int64) (map[string]any, error) {
	row := m.currentRows[fmt.Sprintf("%s|%d", table, pkValue)]
	result := make(map[string]any, len(columns))
	for _, c := range columns {
		result[c] = row[c]
	}
	return result, nil
}

func (m *mockDBAdapter) LookupValue(_ context.Context, table, returnCol, whereCol string, whereVal any) (any, error) {
	key := fmt.Sprintf("%s.%s.%s=%v", table, returnCol, whereCol, whereVal)
	val, ok := m.lookupData[key]
	if !ok {
		// Wrap the same sentinel as the real adapters so errors.Is checks
		// behave identically in tests.
		return nil, fmt.Errorf("%w: %s.%s where %s=%v", domain.ErrLookupNotFound, table, returnCol, whereCol, whereVal)
	}
	return val, nil
}

// failingDBAdapter returns an error on InsertRow for testing error propagation.
type failingDBAdapter struct {
	mockDBAdapter
}

func (f *failingDBAdapter) InsertRow(_ context.Context, _ string, _ map[string]any, _ string) (int64, error) {
	return 0, fmt.Errorf("insert failed")
}

func TestInsertGraphAction(t *testing.T) {
	t.Run("it inserts a single entity and stores its ref ID", func(t *testing.T) {
		db := newMockDBAdapter()
		refMap := make(map[string]int64)

		entities := []domain.Entity{
			{
				Table: "users",
				RefID: "user1",
				Columns: map[string]any{
					"name":  "Alice",
					"email": "alice@test.com",
				},
			},
		}

		err := (&InsertGraphAction{DB: db, Entities: entities, RefMap: refMap}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(db.insertedRows) != 1 {
			t.Fatalf("expected 1 insert, got %d", len(db.insertedRows))
		}

		if db.insertedRows[0].Table != "users" {
			t.Errorf("expected table 'users', got %q", db.insertedRows[0].Table)
		}

		if refMap["user1"] != 1 {
			t.Errorf("expected refMap['user1'] = 1, got %d", refMap["user1"])
		}
	})

	t.Run("it inserts parent then child with resolved ref ID", func(t *testing.T) {
		db := newMockDBAdapter()
		refMap := make(map[string]int64)

		entities := []domain.Entity{
			{
				Table:   "persons",
				RefID:   "person1",
				Columns: map[string]any{"name": "Test"},
				Children: []domain.Entity{
					{
						Table:   "identities",
						RefID:   "identity1",
						Columns: map[string]any{"person_id": "{{ person1.id }}", "type": "email"},
					},
				},
			},
		}

		err := (&InsertGraphAction{DB: db, Entities: entities, RefMap: refMap}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(db.insertedRows) != 2 {
			t.Fatalf("expected 2 inserts, got %d", len(db.insertedRows))
		}

		if db.insertedRows[0].Table != "persons" {
			t.Errorf("expected first insert to 'persons', got %q", db.insertedRows[0].Table)
		}

		if db.insertedRows[1].Table != "identities" {
			t.Errorf("expected second insert to 'identities', got %q", db.insertedRows[1].Table)
		}

		childPersonID, ok := db.insertedRows[1].Columns["person_id"].(int64)
		if !ok {
			t.Fatalf("expected person_id to be int64, got %T", db.insertedRows[1].Columns["person_id"])
		}

		if childPersonID != 1 {
			t.Errorf("expected person_id 1, got %d", childPersonID)
		}

		if refMap["person1"] != 1 {
			t.Errorf("expected refMap['person1'] = 1, got %d", refMap["person1"])
		}

		if refMap["identity1"] != 2 {
			t.Errorf("expected refMap['identity1'] = 2, got %d", refMap["identity1"])
		}
	})

	t.Run("it skips ref map entry when _id is empty", func(t *testing.T) {
		db := newMockDBAdapter()
		refMap := make(map[string]int64)

		entities := []domain.Entity{
			{
				Table:   "settings",
				Columns: map[string]any{"key": "theme", "value": "dark"},
			},
		}

		err := (&InsertGraphAction{DB: db, Entities: entities, RefMap: refMap}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(refMap) != 0 {
			t.Errorf("expected empty refMap, got %v", refMap)
		}
	})

	t.Run("it resolves lookup expressions during insert", func(t *testing.T) {
		db := newMockDBAdapter()
		db.lookupData["industry_types.id.code=RESTAURANT"] = int64(42)

		refMap := make(map[string]int64)

		entities := []domain.Entity{
			{
				Table: "benchmarks",
				Columns: map[string]any{
					"industry_type_id": "{{ lookup|industry_types,id,code=RESTAURANT }}",
					"category":         "TEAMWORK",
					"avg_score":        3.50,
				},
			},
		}

		err := (&InsertGraphAction{DB: db, Entities: entities, RefMap: refMap}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(db.insertedRows) != 1 {
			t.Fatalf("expected 1 insert, got %d", len(db.insertedRows))
		}

		industryID, ok := db.insertedRows[0].Columns["industry_type_id"].(int64)
		if !ok {
			t.Fatalf("expected int64 for industry_type_id, got %T", db.insertedRows[0].Columns["industry_type_id"])
		}

		if industryID != 42 {
			t.Errorf("expected industry_type_id 42, got %d", industryID)
		}
	})

	t.Run("it tracks inserted rows with entity file and insertion order", func(t *testing.T) {
		db := newMockDBAdapter()
		refMap := make(map[string]int64)

		entities := []domain.Entity{
			{
				Table:    "users",
				RefID:    "u1",
				PKColumn: "id",
				Columns:  map[string]any{"name": "Alice"},
			},
		}

		action := &InsertGraphAction{DB: db, Entities: entities, RefMap: refMap, EntityFile: "test.yaml"}
		err := action.Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(action.TrackedRows) != 1 {
			t.Fatalf("expected 1 tracked row, got %d", len(action.TrackedRows))
		}

		row := action.TrackedRows[0]
		if row.EntityFile != "test.yaml" {
			t.Errorf("expected EntityFile 'test.yaml', got %q", row.EntityFile)
		}
		if row.TableName != "users" {
			t.Errorf("expected TableName 'users', got %q", row.TableName)
		}
		if row.RowPK != 1 {
			t.Errorf("expected RowPK 1, got %d", row.RowPK)
		}
		if row.InsertionOrder != 0 {
			t.Errorf("expected InsertionOrder 0, got %d", row.InsertionOrder)
		}
	})

	t.Run("it tracks parent and child rows with correct insertion order", func(t *testing.T) {
		db := newMockDBAdapter()
		refMap := make(map[string]int64)

		entities := []domain.Entity{
			{
				Table:    "users",
				RefID:    "u1",
				PKColumn: "id",
				Columns:  map[string]any{"name": "Alice"},
				Children: []domain.Entity{
					{
						Table:    "profiles",
						RefID:    "p1",
						PKColumn: "id",
						Columns:  map[string]any{"user_id": "{{ u1.id }}", "bio": "test"},
					},
				},
			},
		}

		action := &InsertGraphAction{DB: db, Entities: entities, RefMap: refMap, EntityFile: "graph.yaml"}
		err := action.Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(action.TrackedRows) != 2 {
			t.Fatalf("expected 2 tracked rows, got %d", len(action.TrackedRows))
		}

		if action.TrackedRows[0].TableName != "users" {
			t.Errorf("expected first tracked row table 'users', got %q", action.TrackedRows[0].TableName)
		}
		if action.TrackedRows[0].InsertionOrder != 0 {
			t.Errorf("expected first tracked row order 0, got %d", action.TrackedRows[0].InsertionOrder)
		}
		if action.TrackedRows[1].TableName != "profiles" {
			t.Errorf("expected second tracked row table 'profiles', got %q", action.TrackedRows[1].TableName)
		}
		if action.TrackedRows[1].InsertionOrder != 1 {
			t.Errorf("expected second tracked row order 1, got %d", action.TrackedRows[1].InsertionOrder)
		}
	})

	t.Run("it does not track rows when EntityFile is empty", func(t *testing.T) {
		db := newMockDBAdapter()
		refMap := make(map[string]int64)

		entities := []domain.Entity{
			{
				Table:    "users",
				PKColumn: "id",
				Columns:  map[string]any{"name": "Alice"},
			},
		}

		action := &InsertGraphAction{DB: db, Entities: entities, RefMap: refMap}
		err := action.Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if action.TrackedRows != nil {
			t.Errorf("expected nil TrackedRows when EntityFile is empty, got %v", action.TrackedRows)
		}
	})

	t.Run("it propagates insert errors", func(t *testing.T) {
		db := &failingDBAdapter{mockDBAdapter: *newMockDBAdapter()}
		refMap := make(map[string]int64)

		entities := []domain.Entity{
			{
				Table:   "users",
				Columns: map[string]any{"name": "Alice"},
			},
		}

		err := (&InsertGraphAction{DB: db, Entities: entities, RefMap: refMap}).Execute(context.Background())
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}
