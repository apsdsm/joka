package app

import (
	"context"
	"fmt"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// mockDBAdapter is a hand-rolled mock for the DBAdapter interface.
type mockDBAdapter struct {
	insertedRows    []mockInsertCall
	nextID          int64
	trackingRows    []string
	synced          map[string]bool
	lookupData      map[string]any // keyed by "table.returnCol.whereCol=whereVal"
	entityRows      []domain.TrackedRow
	entityHashes    map[string]string
	deletedRows     []mockDeleteCall
	deletedTracking []string
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

func newMockDBAdapter() *mockDBAdapter {
	return &mockDBAdapter{
		nextID:       1,
		synced:       make(map[string]bool),
		lookupData:   make(map[string]any),
		entityHashes: make(map[string]string),
	}
}

func (m *mockDBAdapter) EnsureTrackingTable(_ context.Context) error    { return nil }
func (m *mockDBAdapter) EnsureRowTrackingTable(_ context.Context) error { return nil }
func (m *mockDBAdapter) EnsureContentHashColumn(_ context.Context) error {
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

func (m *mockDBAdapter) RecordEntitySyncedWithHash(_ context.Context, filePath, contentHash string) error {
	m.trackingRows = append(m.trackingRows, filePath)
	m.synced[filePath] = true
	m.entityHashes[filePath] = contentHash
	return nil
}

func (m *mockDBAdapter) UpdateEntitySynced(_ context.Context, filePath, contentHash string) error {
	m.entityHashes[filePath] = contentHash
	return nil
}

func (m *mockDBAdapter) GetEntityHash(_ context.Context, filePath string) (string, error) {
	return m.entityHashes[filePath], nil
}

func (m *mockDBAdapter) GetAllSyncedEntities(_ context.Context) (map[string]string, error) {
	result := make(map[string]string)
	for k, v := range m.entityHashes {
		result[k] = v
	}
	return result, nil
}

func (m *mockDBAdapter) RecordEntityRow(_ context.Context, row domain.TrackedRow) error {
	m.entityRows = append(m.entityRows, row)
	return nil
}

func (m *mockDBAdapter) GetTrackedRows(_ context.Context, entityFile string) ([]domain.TrackedRow, error) {
	var rows []domain.TrackedRow
	for i := len(m.entityRows) - 1; i >= 0; i-- {
		if m.entityRows[i].EntityFile == entityFile {
			rows = append(rows, m.entityRows[i])
		}
	}
	return rows, nil
}

func (m *mockDBAdapter) DeleteTrackedRows(_ context.Context, entityFile string) error {
	m.deletedTracking = append(m.deletedTracking, entityFile)
	var remaining []domain.TrackedRow
	for _, r := range m.entityRows {
		if r.EntityFile != entityFile {
			remaining = append(remaining, r)
		}
	}
	m.entityRows = remaining
	return nil
}

func (m *mockDBAdapter) DeleteRow(_ context.Context, table, pkColumn string, pkValue int64) error {
	m.deletedRows = append(m.deletedRows, mockDeleteCall{Table: table, PKColumn: pkColumn, PKValue: pkValue})
	return nil
}

func (m *mockDBAdapter) DeleteEntityRecord(_ context.Context, filePath string) error {
	delete(m.synced, filePath)
	delete(m.entityHashes, filePath)
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

	err := (&InsertGraphAction{DB: db, Entities: entities, RefMap: refMap}).Execute(context.Background())
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

	err := (&InsertGraphAction{DB: db, Entities: entities, RefMap: refMap}).Execute(context.Background())
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
}

func TestInsertGraphAction_TracksRows(t *testing.T) {
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
}

func TestInsertGraphAction_TracksParentAndChild(t *testing.T) {
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
}

func TestInsertGraphAction_NoTrackingWithoutEntityFile(t *testing.T) {
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

	err := (&InsertGraphAction{DB: db, Entities: entities, RefMap: refMap}).Execute(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
