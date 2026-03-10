package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// failingRecordRowAdapter returns an error on RecordEntityRow.
type failingRecordRowAdapter struct {
	mockDBAdapter
}

func (f *failingRecordRowAdapter) RecordEntityRow(_ context.Context, _ domain.TrackedRow) error {
	return fmt.Errorf("record row failed")
}

func TestUpdateEntityAction(t *testing.T) {
	t.Run("it skips tracked entities and inserts only new ones", func(t *testing.T) {
		dir := t.TempDir()
		yamlContent := `entities:
  - _is: users
    _id: admin
    name: Admin
    _has:
      - _is: api_keys
        _id: existing_key
        user_id: "{{ admin.id }}"
        key: "old-key"
      - _is: api_keys
        _id: new_key
        user_id: "{{ admin.id }}"
        key: "new-key"
`
		fullPath := filepath.Join(dir, "admin.yaml")
		os.WriteFile(fullPath, []byte(yamlContent), 0644)

		db := newMockDBAdapter()
		db.synced["admin.yaml"] = true
		db.entityHashes["admin.yaml"] = "old_hash"
		db.entityRows = []domain.TrackedRow{
			{EntityFile: "admin.yaml", TableName: "users", RowPK: 42, PKColumn: "id", RefID: "admin", InsertionOrder: 0},
			{EntityFile: "admin.yaml", TableName: "api_keys", RowPK: 87, PKColumn: "id", RefID: "existing_key", InsertionOrder: 1},
		}

		result, err := (UpdateEntityAction{
			DB:          db,
			FilePath:    "admin.yaml",
			FullPath:    fullPath,
			ContentHash: "new_hash",
		}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Should only insert the new key, not the admin or existing key.
		if len(db.insertedRows) != 1 {
			t.Fatalf("expected 1 insert, got %d", len(db.insertedRows))
		}
		if db.insertedRows[0].Table != "api_keys" {
			t.Errorf("expected insert into 'api_keys', got %q", db.insertedRows[0].Table)
		}

		// The new key should have resolved {{ admin.id }} to 42.
		userID, ok := db.insertedRows[0].Columns["user_id"].(int64)
		if !ok {
			t.Fatalf("expected user_id to be int64, got %T", db.insertedRows[0].Columns["user_id"])
		}
		if userID != 42 {
			t.Errorf("expected user_id 42, got %d", userID)
		}

		// Result should report skipped and inserted.
		if len(result.Skipped) != 2 {
			t.Errorf("expected 2 skipped, got %d", len(result.Skipped))
		}
		if len(result.Inserted) != 1 {
			t.Errorf("expected 1 inserted, got %d", len(result.Inserted))
		}

		// Hash should be updated.
		if db.entityHashes["admin.yaml"] != "new_hash" {
			t.Errorf("expected hash 'new_hash', got %q", db.entityHashes["admin.yaml"])
		}
	})

	t.Run("it returns ErrEntityNotSynced when file was never synced", func(t *testing.T) {
		db := newMockDBAdapter()

		_, err := (UpdateEntityAction{
			DB:       db,
			FilePath: "unknown.yaml",
			FullPath: "/tmp/unknown.yaml",
		}).Execute(context.Background())
		if err == nil {
			t.Fatal("expected error, got nil")
		}

		if !errors.Is(err, domain.ErrEntityNotSynced) {
			t.Errorf("expected ErrEntityNotSynced, got: %v", err)
		}
	})

	t.Run("it returns ErrEntityMissingRefID when entity has no _id", func(t *testing.T) {
		dir := t.TempDir()
		yamlContent := `entities:
  - _is: users
    name: Admin
`
		fullPath := filepath.Join(dir, "no_id.yaml")
		os.WriteFile(fullPath, []byte(yamlContent), 0644)

		db := newMockDBAdapter()
		db.synced["no_id.yaml"] = true
		db.entityHashes["no_id.yaml"] = "hash"

		_, err := (UpdateEntityAction{
			DB:          db,
			FilePath:    "no_id.yaml",
			FullPath:    fullPath,
			ContentHash: "hash",
		}).Execute(context.Background())
		if err == nil {
			t.Fatal("expected error, got nil")
		}

		if !errors.Is(err, domain.ErrEntityMissingRefID) {
			t.Errorf("expected ErrEntityMissingRefID, got: %v", err)
		}
	})

	t.Run("it tracks new rows with correct insertion order continuing from existing", func(t *testing.T) {
		dir := t.TempDir()
		yamlContent := `entities:
  - _is: users
    _id: admin
    name: Admin
    _has:
      - _is: api_keys
        _id: new_key
        user_id: "{{ admin.id }}"
        key: "new"
`
		fullPath := filepath.Join(dir, "order.yaml")
		os.WriteFile(fullPath, []byte(yamlContent), 0644)

		db := newMockDBAdapter()
		db.synced["order.yaml"] = true
		db.entityHashes["order.yaml"] = "old"
		db.entityRows = []domain.TrackedRow{
			{EntityFile: "order.yaml", TableName: "users", RowPK: 10, PKColumn: "id", RefID: "admin", InsertionOrder: 0},
		}

		result, err := (UpdateEntityAction{
			DB:          db,
			FilePath:    "order.yaml",
			FullPath:    fullPath,
			ContentHash: "new",
		}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(result.Inserted) != 1 {
			t.Fatalf("expected 1 inserted, got %d", len(result.Inserted))
		}

		// The new row should have insertion_order = 1 (continuing from max 0).
		if len(db.entityRows) != 2 {
			t.Fatalf("expected 2 tracked rows total, got %d", len(db.entityRows))
		}
		newRow := db.entityRows[1]
		if newRow.InsertionOrder != 1 {
			t.Errorf("expected InsertionOrder 1, got %d", newRow.InsertionOrder)
		}
		if newRow.RefID != "new_key" {
			t.Errorf("expected RefID 'new_key', got %q", newRow.RefID)
		}
	})

	t.Run("it handles all entities already tracked with no inserts", func(t *testing.T) {
		dir := t.TempDir()
		yamlContent := `entities:
  - _is: users
    _id: admin
    name: Admin
`
		fullPath := filepath.Join(dir, "all_tracked.yaml")
		os.WriteFile(fullPath, []byte(yamlContent), 0644)

		db := newMockDBAdapter()
		db.synced["all_tracked.yaml"] = true
		db.entityHashes["all_tracked.yaml"] = "old"
		db.entityRows = []domain.TrackedRow{
			{EntityFile: "all_tracked.yaml", TableName: "users", RowPK: 1, PKColumn: "id", RefID: "admin", InsertionOrder: 0},
		}

		result, err := (UpdateEntityAction{
			DB:          db,
			FilePath:    "all_tracked.yaml",
			FullPath:    fullPath,
			ContentHash: "new",
		}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(db.insertedRows) != 0 {
			t.Errorf("expected 0 inserts, got %d", len(db.insertedRows))
		}
		if len(result.Skipped) != 1 {
			t.Errorf("expected 1 skipped, got %d", len(result.Skipped))
		}
		if len(result.Inserted) != 0 {
			t.Errorf("expected 0 inserted, got %d", len(result.Inserted))
		}
	})

	t.Run("it propagates insert errors from the graph action", func(t *testing.T) {
		dir := t.TempDir()
		yamlContent := `entities:
  - _is: users
    _id: admin
    name: Admin
    _has:
      - _is: api_keys
        _id: new_key
        user_id: "{{ admin.id }}"
        key: "new"
`
		fullPath := filepath.Join(dir, "insert_fail.yaml")
		os.WriteFile(fullPath, []byte(yamlContent), 0644)

		db := &failingDBAdapter{}
		db.synced = map[string]bool{"insert_fail.yaml": true}
		db.entityHashes = map[string]string{"insert_fail.yaml": "hash"}
		db.entityRows = []domain.TrackedRow{
			{EntityFile: "insert_fail.yaml", TableName: "users", RowPK: 1, PKColumn: "id", RefID: "admin", InsertionOrder: 0},
		}

		_, err := (UpdateEntityAction{
			DB:          db,
			FilePath:    "insert_fail.yaml",
			FullPath:    fullPath,
			ContentHash: "new",
		}).Execute(context.Background())
		if err == nil {
			t.Fatal("expected error, got nil")
		}

		if !strings.Contains(err.Error(), "insert failed") {
			t.Errorf("expected insert error, got: %v", err)
		}
	})

	t.Run("it returns ErrDuplicateRefID when YAML has duplicate _id handles", func(t *testing.T) {
		dir := t.TempDir()
		yamlContent := `entities:
  - _is: users
    _id: dupe
    name: Alice
  - _is: users
    _id: dupe
    name: Bob
`
		fullPath := filepath.Join(dir, "dup.yaml")
		os.WriteFile(fullPath, []byte(yamlContent), 0644)

		db := newMockDBAdapter()
		db.synced["dup.yaml"] = true
		db.entityHashes["dup.yaml"] = "hash"

		_, err := (UpdateEntityAction{
			DB:          db,
			FilePath:    "dup.yaml",
			FullPath:    fullPath,
			ContentHash: "hash",
		}).Execute(context.Background())
		if err == nil {
			t.Fatal("expected error, got nil")
		}

		if !errors.Is(err, domain.ErrDuplicateRefID) {
			t.Errorf("expected ErrDuplicateRefID, got: %v", err)
		}
	})

	t.Run("it returns parse error for invalid YAML", func(t *testing.T) {
		dir := t.TempDir()
		fullPath := filepath.Join(dir, "bad.yaml")
		os.WriteFile(fullPath, []byte("not: [valid: yaml: {{{"), 0644)

		db := newMockDBAdapter()
		db.synced["bad.yaml"] = true
		db.entityHashes["bad.yaml"] = "hash"

		_, err := (UpdateEntityAction{
			DB:          db,
			FilePath:    "bad.yaml",
			FullPath:    fullPath,
			ContentHash: "hash",
		}).Execute(context.Background())
		if err == nil {
			t.Fatal("expected error, got nil")
		}

		if !errors.Is(err, domain.ErrEntityParseFailed) {
			t.Errorf("expected ErrEntityParseFailed, got: %v", err)
		}
	})

	t.Run("it returns ErrEntityMissingRefID when nested child has no _id", func(t *testing.T) {
		dir := t.TempDir()
		yamlContent := `entities:
  - _is: users
    _id: admin
    name: Admin
    _has:
      - _is: api_keys
        key: "no-ref-id"
`
		fullPath := filepath.Join(dir, "child_no_id.yaml")
		os.WriteFile(fullPath, []byte(yamlContent), 0644)

		db := newMockDBAdapter()
		db.synced["child_no_id.yaml"] = true
		db.entityHashes["child_no_id.yaml"] = "hash"

		_, err := (UpdateEntityAction{
			DB:          db,
			FilePath:    "child_no_id.yaml",
			FullPath:    fullPath,
			ContentHash: "hash",
		}).Execute(context.Background())
		if err == nil {
			t.Fatal("expected error, got nil")
		}

		if !errors.Is(err, domain.ErrEntityMissingRefID) {
			t.Errorf("expected ErrEntityMissingRefID, got: %v", err)
		}
	})

	t.Run("it propagates RecordEntityRow errors", func(t *testing.T) {
		dir := t.TempDir()
		yamlContent := `entities:
  - _is: users
    _id: admin
    name: Admin
    _has:
      - _is: api_keys
        _id: new_key
        user_id: "{{ admin.id }}"
        key: "new"
`
		fullPath := filepath.Join(dir, "record_fail.yaml")
		os.WriteFile(fullPath, []byte(yamlContent), 0644)

		db := &failingRecordRowAdapter{}
		db.synced = map[string]bool{"record_fail.yaml": true}
		db.entityHashes = map[string]string{"record_fail.yaml": "hash"}
		db.entityRows = []domain.TrackedRow{
			{EntityFile: "record_fail.yaml", TableName: "users", RowPK: 1, PKColumn: "id", RefID: "admin", InsertionOrder: 0},
		}

		_, err := (UpdateEntityAction{
			DB:          db,
			FilePath:    "record_fail.yaml",
			FullPath:    fullPath,
			ContentHash: "new",
		}).Execute(context.Background())
		if err == nil {
			t.Fatal("expected error, got nil")
		}

		if !strings.Contains(err.Error(), "record row failed") {
			t.Errorf("expected record row error, got: %v", err)
		}
	})

	t.Run("it inserts new child that references new parent both are new", func(t *testing.T) {
		dir := t.TempDir()
		yamlContent := `entities:
  - _is: users
    _id: existing_user
    name: Existing
    _has:
      - _is: teams
        _id: new_team
        owner_id: "{{ existing_user.id }}"
        name: "New Team"
        _has:
          - _is: memberships
            _id: new_membership
            team_id: "{{ new_team.id }}"
            user_id: "{{ existing_user.id }}"
`
		fullPath := filepath.Join(dir, "nested.yaml")
		os.WriteFile(fullPath, []byte(yamlContent), 0644)

		db := newMockDBAdapter()
		db.synced["nested.yaml"] = true
		db.entityHashes["nested.yaml"] = "old"
		db.entityRows = []domain.TrackedRow{
			{EntityFile: "nested.yaml", TableName: "users", RowPK: 5, PKColumn: "id", RefID: "existing_user", InsertionOrder: 0},
		}

		result, err := (UpdateEntityAction{
			DB:          db,
			FilePath:    "nested.yaml",
			FullPath:    fullPath,
			ContentHash: "new",
		}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// User skipped, team and membership inserted.
		if len(result.Skipped) != 1 {
			t.Errorf("expected 1 skipped, got %d", len(result.Skipped))
		}
		if len(result.Inserted) != 2 {
			t.Errorf("expected 2 inserted, got %d", len(result.Inserted))
		}
		if len(db.insertedRows) != 2 {
			t.Fatalf("expected 2 inserts, got %d", len(db.insertedRows))
		}

		// Team should reference existing user PK (5).
		teamOwnerID, ok := db.insertedRows[0].Columns["owner_id"].(int64)
		if !ok {
			t.Fatalf("expected owner_id int64, got %T", db.insertedRows[0].Columns["owner_id"])
		}
		if teamOwnerID != 5 {
			t.Errorf("expected owner_id 5, got %d", teamOwnerID)
		}

		// Membership should reference the newly inserted team.
		membershipTeamID, ok := db.insertedRows[1].Columns["team_id"].(int64)
		if !ok {
			t.Fatalf("expected team_id int64, got %T", db.insertedRows[1].Columns["team_id"])
		}
		if membershipTeamID != 1 {
			t.Errorf("expected team_id 1 (first auto-increment), got %d", membershipTeamID)
		}
	})
}

func TestInsertGraphAction_SkipRefIDs(t *testing.T) {
	t.Run("it skips entities in SkipRefIDs and loads their PK into RefMap", func(t *testing.T) {
		db := newMockDBAdapter()
		refMap := make(map[string]int64)

		entities := []domain.Entity{
			{
				Table:    "users",
				RefID:    "admin",
				PKColumn: "id",
				Columns:  map[string]any{"name": "Admin"},
				Children: []domain.Entity{
					{
						Table:    "profiles",
						RefID:    "profile1",
						PKColumn: "id",
						Columns:  map[string]any{"user_id": "{{ admin.id }}", "bio": "test"},
					},
				},
			},
		}

		skipRefIDs := map[string]int64{"admin": 42}

		action := &InsertGraphAction{
			DB:         db,
			Entities:   entities,
			RefMap:     refMap,
			SkipRefIDs: skipRefIDs,
			EntityFile: "test.yaml",
		}
		err := action.Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Only profile should be inserted, not admin.
		if len(db.insertedRows) != 1 {
			t.Fatalf("expected 1 insert, got %d", len(db.insertedRows))
		}
		if db.insertedRows[0].Table != "profiles" {
			t.Errorf("expected insert into 'profiles', got %q", db.insertedRows[0].Table)
		}

		// admin's PK should be in refMap from skip.
		if refMap["admin"] != 42 {
			t.Errorf("expected refMap['admin'] = 42, got %d", refMap["admin"])
		}

		// profile's user_id should resolve to 42.
		userID, ok := db.insertedRows[0].Columns["user_id"].(int64)
		if !ok {
			t.Fatalf("expected user_id to be int64, got %T", db.insertedRows[0].Columns["user_id"])
		}
		if userID != 42 {
			t.Errorf("expected user_id 42, got %d", userID)
		}

		// Only profile should be tracked.
		if len(action.TrackedRows) != 1 {
			t.Fatalf("expected 1 tracked row, got %d", len(action.TrackedRows))
		}
		if action.TrackedRows[0].TableName != "profiles" {
			t.Errorf("expected tracked row for 'profiles', got %q", action.TrackedRows[0].TableName)
		}
	})

	t.Run("it does not skip entities without _id even when SkipRefIDs is set", func(t *testing.T) {
		db := newMockDBAdapter()
		refMap := make(map[string]int64)

		entities := []domain.Entity{
			{
				Table:    "settings",
				PKColumn: "id",
				Columns:  map[string]any{"key": "theme"},
			},
		}

		skipRefIDs := map[string]int64{"other": 1}

		action := &InsertGraphAction{
			DB:         db,
			Entities:   entities,
			RefMap:     refMap,
			SkipRefIDs: skipRefIDs,
		}
		err := action.Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(db.insertedRows) != 1 {
			t.Fatalf("expected 1 insert, got %d", len(db.insertedRows))
		}
	})
}
