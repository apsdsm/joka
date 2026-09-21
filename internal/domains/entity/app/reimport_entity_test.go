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
        _id: admin_profile
        user_id: "{{ admin.id }}"
        bio: "Bio"
`
		fullPath := filepath.Join(dir, "admin.yaml")
		os.WriteFile(fullPath, []byte(yamlContent), 0644)

		db := newMockDBAdapter()
		db.trackHash("admin.yaml", "old_hash",
			domain.TrackedRow{TableName: "users", RowPK: 1, PKColumn: "id", RefID: "admin", InsertionOrder: 0},
			domain.TrackedRow{TableName: "profiles", RowPK: 2, PKColumn: "id", RefID: "admin_profile", InsertionOrder: 1},
		)

		err := (ReimportEntityAction{
			DB:          db,
			Backend:     db.backend(),
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

		if len(db.insertedRows) != 2 {
			t.Errorf("expected 2 re-inserts, got %d", len(db.insertedRows))
		}

		// The tracking points at the rows just inserted, not the ones deleted.
		rows := db.trackedRows()
		if len(rows) != 2 {
			t.Fatalf("expected 2 tracked rows after the reimport, got %d", len(rows))
		}
		if rows[0].RowPK != 1 || rows[1].RowPK != 2 {
			t.Errorf("expected the tracking re-pointed at the new rows, got %+v", rows)
		}

		if hash, _ := db.fileHash("admin.yaml"); hash != "new_hash" {
			t.Errorf("expected updated hash 'new_hash', got %q", hash)
		}
	})

	t.Run("it returns ErrEntityNotSynced when file was never synced", func(t *testing.T) {
		db := newMockDBAdapter()

		err := (ReimportEntityAction{
			DB:       db,
			Backend:  db.backend(),
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

		db := &fkErrorDBAdapter{mockDBAdapter: *newMockDBAdapter()}
		db.track("fk.yaml",
			domain.TrackedRow{TableName: "users", RowPK: 1, PKColumn: "id", RefID: "a", InsertionOrder: 0},
		)

		err := (ReimportEntityAction{
			DB:       db,
			Backend:  db.backend(),
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
    _id: admin
    name: Admin
`
		fullPath := filepath.Join(dir, "empty.yaml")
		os.WriteFile(fullPath, []byte(yamlContent), 0644)

		db := newMockDBAdapter()
		db.trackHash("empty.yaml", "old")

		err := (ReimportEntityAction{
			DB:          db,
			Backend:     db.backend(),
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

		if hash, _ := db.fileHash("empty.yaml"); hash != "new" {
			t.Errorf("expected hash 'new', got %q", hash)
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
