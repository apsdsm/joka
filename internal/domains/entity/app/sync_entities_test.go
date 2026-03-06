package app

import (
	"context"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

func TestSyncEntitiesAction(t *testing.T) {
	t.Run("it inserts and tracks new entity files", func(t *testing.T) {
		db := newMockDBAdapter()

		files := []*domain.EntityFile{
			{
				Path:        "users.yaml",
				ContentHash: "abc123",
				Entities: []domain.Entity{
					{Table: "users", RefID: "u1", PKColumn: "id", Columns: map[string]any{"name": "Alice"}},
				},
			},
			{
				Path:        "settings.yaml",
				ContentHash: "def456",
				Entities: []domain.Entity{
					{Table: "settings", PKColumn: "id", Columns: map[string]any{"key": "theme"}},
				},
			},
		}

		synced, err := (SyncEntitiesAction{DB: db, Files: files}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(synced) != 2 {
			t.Fatalf("expected 2 synced files, got %d", len(synced))
		}

		if synced[0] != "users.yaml" || synced[1] != "settings.yaml" {
			t.Errorf("unexpected synced paths: %v", synced)
		}

		if len(db.insertedRows) != 2 {
			t.Errorf("expected 2 inserts, got %d", len(db.insertedRows))
		}

		if len(db.entityRows) != 2 {
			t.Errorf("expected 2 tracked rows via RecordEntityRow, got %d", len(db.entityRows))
		}

		if db.entityHashes["users.yaml"] != "abc123" {
			t.Errorf("expected hash 'abc123' for users.yaml, got %q", db.entityHashes["users.yaml"])
		}
	})

	t.Run("it skips already-synced files", func(t *testing.T) {
		db := newMockDBAdapter()
		db.synced["existing.yaml"] = true

		files := []*domain.EntityFile{
			{
				Path:        "existing.yaml",
				ContentHash: "abc",
				Entities: []domain.Entity{
					{Table: "users", PKColumn: "id", Columns: map[string]any{"name": "Alice"}},
				},
			},
		}

		synced, err := (SyncEntitiesAction{DB: db, Files: files}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(synced) != 0 {
			t.Errorf("expected 0 synced files, got %d", len(synced))
		}

		if len(db.insertedRows) != 0 {
			t.Errorf("expected 0 inserts, got %d", len(db.insertedRows))
		}
	})

	t.Run("it returns an error for duplicate ref IDs", func(t *testing.T) {
		db := newMockDBAdapter()

		files := []*domain.EntityFile{
			{
				Path:        "bad.yaml",
				ContentHash: "abc",
				Entities: []domain.Entity{
					{Table: "users", RefID: "dup", PKColumn: "id", Columns: map[string]any{"name": "A"}},
					{Table: "users", RefID: "dup", PKColumn: "id", Columns: map[string]any{"name": "B"}},
				},
			},
		}

		_, err := (SyncEntitiesAction{DB: db, Files: files}).Execute(context.Background())
		if err == nil {
			t.Fatal("expected error for duplicate ref IDs, got nil")
		}

		if len(db.insertedRows) != 0 {
			t.Errorf("expected 0 inserts after validation failure, got %d", len(db.insertedRows))
		}
	})

	t.Run("it stores the content hash when provided", func(t *testing.T) {
		db := newMockDBAdapter()

		files := []*domain.EntityFile{
			{
				Path:        "hashed.yaml",
				ContentHash: "sha256hex",
				Entities: []domain.Entity{
					{Table: "users", PKColumn: "id", Columns: map[string]any{"name": "A"}},
				},
			},
		}

		_, err := (SyncEntitiesAction{DB: db, Files: files}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if db.entityHashes["hashed.yaml"] != "sha256hex" {
			t.Errorf("expected hash 'sha256hex', got %q", db.entityHashes["hashed.yaml"])
		}
	})

	t.Run("it falls back to RecordEntitySynced when hash is empty", func(t *testing.T) {
		db := newMockDBAdapter()

		files := []*domain.EntityFile{
			{
				Path:        "nohash.yaml",
				ContentHash: "",
				Entities: []domain.Entity{
					{Table: "users", PKColumn: "id", Columns: map[string]any{"name": "A"}},
				},
			},
		}

		_, err := (SyncEntitiesAction{DB: db, Files: files}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !db.synced["nohash.yaml"] {
			t.Error("expected file to be marked as synced")
		}

		if db.entityHashes["nohash.yaml"] != "" {
			t.Errorf("expected empty hash for fallback path, got %q", db.entityHashes["nohash.yaml"])
		}
	})
}
