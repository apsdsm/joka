package app

import (
	"context"
	"fmt"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// mockDBAdapter is a hand-rolled mock for the DBAdapter interface.
type mockDBAdapter struct {
	insertedRows []mockInsertCall
	nextID       int64
	trackingRows []string
	synced       map[string]bool
	lookupData   map[string]any // keyed by "table.returnCol.whereCol=whereVal"
}

// mockInsertCall records the arguments passed to InsertRow.
type mockInsertCall struct {
	Table   string
	Columns map[string]any
}

func newMockDBAdapter() *mockDBAdapter {
	return &mockDBAdapter{
		nextID:     1,
		synced:     make(map[string]bool),
		lookupData: make(map[string]any),
	}
}

func (m *mockDBAdapter) EnsureTrackingTable(_ context.Context) error {
	return nil
}

func (m *mockDBAdapter) IsEntitySynced(_ context.Context, filePath string) (bool, error) {
	return m.synced[filePath], nil
}

func (m *mockDBAdapter) RecordEntitySynced(_ context.Context, filePath string) error {
	m.trackingRows = append(m.trackingRows, filePath)
	m.synced[filePath] = true

	return nil
}

func (m *mockDBAdapter) InsertRow(_ context.Context, table string, columns map[string]any, _ string) (int64, error) {
	m.insertedRows = append(m.insertedRows, mockInsertCall{Table: table, Columns: columns})
	id := m.nextID
	m.nextID++

	return id, nil
}

func (m *mockDBAdapter) LookupValue(_ context.Context, table, returnCol, whereCol string, whereVal any) (any, error) {
	key := fmt.Sprintf("%s.%s.%s=%v", table, returnCol, whereCol, whereVal)

	val, ok := m.lookupData[key]
	if !ok {
		return nil, fmt.Errorf("lookup returned no rows: %s.%s where %s=%v", table, returnCol, whereCol, whereVal)
	}

	return val, nil
}

func TestInsertGraphAction_SingleEntity(t *testing.T) {
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

	err := InsertGraphAction{DB: db, Entities: entities, RefMap: refMap}.Execute(context.Background())
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
}

func TestInsertGraphAction_ParentChild(t *testing.T) {
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

	err := InsertGraphAction{DB: db, Entities: entities, RefMap: refMap}.Execute(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(db.insertedRows) != 2 {
		t.Fatalf("expected 2 inserts, got %d", len(db.insertedRows))
	}

	// First insert is the parent.
	if db.insertedRows[0].Table != "persons" {
		t.Errorf("expected first insert to 'persons', got %q", db.insertedRows[0].Table)
	}

	// Second insert is the child with resolved parent id.
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
}

func TestInsertGraphAction_NoRefID(t *testing.T) {
	db := newMockDBAdapter()
	refMap := make(map[string]int64)

	entities := []domain.Entity{
		{
			Table:   "settings",
			Columns: map[string]any{"key": "theme", "value": "dark"},
		},
	}

	err := InsertGraphAction{DB: db, Entities: entities, RefMap: refMap}.Execute(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(refMap) != 0 {
		t.Errorf("expected empty refMap, got %v", refMap)
	}
}

// failingDBAdapter returns an error on InsertRow for testing error propagation.
type failingDBAdapter struct {
	mockDBAdapter
}

func (f *failingDBAdapter) InsertRow(_ context.Context, _ string, _ map[string]any, _ string) (int64, error) {
	return 0, fmt.Errorf("insert failed")
}

func TestInsertGraphAction_WithLookup(t *testing.T) {
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

	err := InsertGraphAction{DB: db, Entities: entities, RefMap: refMap}.Execute(context.Background())
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
}

func TestInsertGraphAction_InsertError(t *testing.T) {
	db := &failingDBAdapter{}
	refMap := make(map[string]int64)

	entities := []domain.Entity{
		{
			Table:   "users",
			Columns: map[string]any{"name": "Alice"},
		},
	}

	err := InsertGraphAction{DB: db, Entities: entities, RefMap: refMap}.Execute(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
