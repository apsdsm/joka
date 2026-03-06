package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

func TestReimportEntityAction(t *testing.T) {
	t.Run("it deletes old rows in reverse order and re-inserts from YAML", func(t *testing.T) {
		dir := t.TempDir()
		yamlContent := `entities:
  - _is: users
    _id: admin
    name: Admin
    _has:
      - _is: profiles
        user_id: "{{ admin.id }}"
        bio: "Bio"
`
		fullPath := filepath.Join(dir, "admin.yaml")
		os.WriteFile(fullPath, []byte(yamlContent), 0644)

		db := newMockDBAdapter()
		db.synced["admin.yaml"] = true
		db.entityHashes["admin.yaml"] = "old_hash"
		db.entityRows = []domain.TrackedRow{
			{EntityFile: "admin.yaml", TableName: "users", RowPK: 1, PKColumn: "id", InsertionOrder: 0},
			{EntityFile: "admin.yaml", TableName: "profiles", RowPK: 2, PKColumn: "id", InsertionOrder: 1},
		}

		err := (ReimportEntityAction{
			DB:          db,
			FilePath:    "admin.yaml",
			FullPath:    fullPath,
			ContentHash: "new_hash",
		}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(db.deletedRows) != 2 {
			t.Fatalf("expected 2 deletions, got %d", len(db.deletedRows))
		}
		if db.deletedRows[0].Table != "profiles" {
			t.Errorf("expected first deletion from 'profiles', got %q", db.deletedRows[0].Table)
		}
		if db.deletedRows[1].Table != "users" {
			t.Errorf("expected second deletion from 'users', got %q", db.deletedRows[1].Table)
		}

		if len(db.deletedTracking) != 1 || db.deletedTracking[0] != "admin.yaml" {
			t.Errorf("expected DeleteTrackedRows for admin.yaml, got %v", db.deletedTracking)
		}

		if len(db.insertedRows) != 2 {
			t.Errorf("expected 2 re-inserts, got %d", len(db.insertedRows))
		}

		if db.entityHashes["admin.yaml"] != "new_hash" {
			t.Errorf("expected updated hash 'new_hash', got %q", db.entityHashes["admin.yaml"])
		}
	})

	t.Run("it returns ErrEntityNotSynced when file was never synced", func(t *testing.T) {
		db := newMockDBAdapter()

		err := (ReimportEntityAction{
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

	t.Run("it returns ErrForeignKeyConflict when child rows exist", func(t *testing.T) {
		dir := t.TempDir()
		fullPath := filepath.Join(dir, "fk.yaml")
		os.WriteFile(fullPath, []byte("entities:\n  - _is: users\n    name: A\n"), 0644)

		db := &fkErrorDBAdapter{}
		db.synced = map[string]bool{"fk.yaml": true}
		db.entityHashes = map[string]string{"fk.yaml": "hash"}
		db.entityRows = []domain.TrackedRow{
			{EntityFile: "fk.yaml", TableName: "users", RowPK: 1, PKColumn: "id", InsertionOrder: 0},
		}

		err := (ReimportEntityAction{
			DB:       db,
			FilePath: "fk.yaml",
			FullPath: fullPath,
		}).Execute(context.Background())
		if err == nil {
			t.Fatal("expected error, got nil")
		}

		if !errors.Is(err, domain.ErrForeignKeyConflict) {
			t.Errorf("expected ErrForeignKeyConflict, got: %v", err)
		}
	})

	t.Run("it re-inserts even when there are no previously tracked rows", func(t *testing.T) {
		dir := t.TempDir()
		yamlContent := `entities:
  - _is: users
    name: Admin
`
		fullPath := filepath.Join(dir, "empty.yaml")
		os.WriteFile(fullPath, []byte(yamlContent), 0644)

		db := newMockDBAdapter()
		db.synced["empty.yaml"] = true
		db.entityHashes["empty.yaml"] = "old"

		err := (ReimportEntityAction{
			DB:          db,
			FilePath:    "empty.yaml",
			FullPath:    fullPath,
			ContentHash: "new",
		}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(db.insertedRows) != 1 {
			t.Errorf("expected 1 insert, got %d", len(db.insertedRows))
		}

		if db.entityHashes["empty.yaml"] != "new" {
			t.Errorf("expected hash 'new', got %q", db.entityHashes["empty.yaml"])
		}
	})
}

// fkErrorDBAdapter returns ErrForeignKeyConflict on DeleteRow.
type fkErrorDBAdapter struct {
	mockDBAdapter
}

func (f *fkErrorDBAdapter) DeleteRow(_ context.Context, table, pkColumn string, pkValue int64) error {
	return fmt.Errorf("%w: table %s, %s=%d", domain.ErrForeignKeyConflict, table, pkColumn, pkValue)
}
