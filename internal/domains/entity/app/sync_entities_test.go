package app

import (
	"context"
	"errors"
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

		result, err := (SyncEntitiesAction{DB: db, Files: files}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(result.Synced) != 2 {
			t.Fatalf("expected 2 synced files, got %d", len(result.Synced))
		}

		if result.Synced[0] != "users.yaml" || result.Synced[1] != "settings.yaml" {
			t.Errorf("unexpected synced paths: %v", result.Synced)
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

		result, err := (SyncEntitiesAction{DB: db, Files: files}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(result.Synced) != 0 {
			t.Errorf("expected 0 synced files, got %d", len(result.Synced))
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

	t.Run("it updates a modified file in place and refreshes the hash", func(t *testing.T) {
		db := newMockDBAdapter()
		db.synced["client.yaml"] = true
		db.entityHashes["client.yaml"] = "oldhash"
		db.entityRows = []domain.TrackedRow{
			{EntityFile: "client.yaml", TableName: "clients", RowPK: 4, PKColumn: "id", RefID: "c1", InsertionOrder: 0},
		}

		modified := []*domain.EntityFile{
			{
				Path:        "client.yaml",
				ContentHash: "newhash",
				Entities: []domain.Entity{
					{Table: "clients", RefID: "c1", PKColumn: "id", Columns: map[string]any{"redirect_uris": "new-value"}},
				},
			},
		}

		result, err := (SyncEntitiesAction{DB: db, Modified: modified}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(result.Updated) != 1 || result.Updated[0] != "client.yaml" {
			t.Fatalf("expected client.yaml updated, got %v", result.Updated)
		}

		if len(db.insertedRows) != 0 {
			t.Errorf("expected 0 inserts on update path, got %d", len(db.insertedRows))
		}

		if len(db.updatedRows) != 1 {
			t.Fatalf("expected 1 UpdateRow call, got %d", len(db.updatedRows))
		}

		up := db.updatedRows[0]
		if up.Table != "clients" || up.PKColumn != "id" || up.PKValue != 4 {
			t.Errorf("unexpected update target: %+v", up)
		}
		if up.Columns["redirect_uris"] != "new-value" {
			t.Errorf("expected new redirect_uris, got %v", up.Columns["redirect_uris"])
		}

		if db.entityHashes["client.yaml"] != "newhash" {
			t.Errorf("expected hash refreshed to 'newhash', got %q", db.entityHashes["client.yaml"])
		}
	})

	t.Run("it updates a parent and child graph in place, resolving refs from existing PKs", func(t *testing.T) {
		db := newMockDBAdapter()
		db.synced["graph.yaml"] = true
		db.entityHashes["graph.yaml"] = "oldhash"
		db.entityRows = []domain.TrackedRow{
			{EntityFile: "graph.yaml", TableName: "users", RowPK: 1, PKColumn: "id", RefID: "u1", InsertionOrder: 0},
			{EntityFile: "graph.yaml", TableName: "profiles", RowPK: 2, PKColumn: "id", RefID: "p1", InsertionOrder: 1},
		}

		modified := []*domain.EntityFile{
			{
				Path:        "graph.yaml",
				ContentHash: "newhash",
				Entities: []domain.Entity{
					{
						Table: "users", RefID: "u1", PKColumn: "id", Columns: map[string]any{"name": "Updated"},
						Children: []domain.Entity{
							{Table: "profiles", RefID: "p1", PKColumn: "id", Columns: map[string]any{"user_id": "{{ u1.id }}", "bio": "new"}},
						},
					},
				},
			},
		}

		_, err := (SyncEntitiesAction{DB: db, Modified: modified}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(db.insertedRows) != 0 {
			t.Errorf("expected 0 inserts on update path, got %d", len(db.insertedRows))
		}
		if len(db.updatedRows) != 2 {
			t.Fatalf("expected 2 updates (parent + child), got %d", len(db.updatedRows))
		}

		// The child's {{ u1.id }} must resolve to the parent's existing PK (1).
		var child *mockUpdateCall
		for i := range db.updatedRows {
			if db.updatedRows[i].Table == "profiles" {
				child = &db.updatedRows[i]
			}
		}
		if child == nil {
			t.Fatal("expected a profiles update")
		}
		if child.PKValue != 2 {
			t.Errorf("expected child updated by tracked PK 2, got %d", child.PKValue)
		}
		if child.Columns["user_id"].(int64) != 1 {
			t.Errorf("expected child user_id resolved to 1, got %v", child.Columns["user_id"])
		}
	})

	t.Run("it updates entities that have no _id by position", func(t *testing.T) {
		db := newMockDBAdapter()
		db.synced["client.yaml"] = true
		db.entityRows = []domain.TrackedRow{
			{EntityFile: "client.yaml", TableName: "clients", RowPK: 4, PKColumn: "id", RefID: "", InsertionOrder: 0},
		}

		modified := []*domain.EntityFile{
			{
				Path:        "client.yaml",
				ContentHash: "newhash",
				Entities: []domain.Entity{
					{Table: "clients", PKColumn: "id", Columns: map[string]any{"redirect_uris": "new-value"}},
				},
			},
		}

		_, err := (SyncEntitiesAction{DB: db, Modified: modified}).Execute(context.Background())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(db.updatedRows) != 1 {
			t.Fatalf("expected 1 update for the no-_id entity, got %d", len(db.updatedRows))
		}
		if db.updatedRows[0].PKValue != 4 || db.updatedRows[0].Columns["redirect_uris"] != "new-value" {
			t.Errorf("unexpected update: %+v", db.updatedRows[0])
		}
	})

	t.Run("it reports a structural change when an entity is added to the file", func(t *testing.T) {
		db := newMockDBAdapter()
		db.synced["client.yaml"] = true
		db.entityRows = []domain.TrackedRow{
			{EntityFile: "client.yaml", TableName: "clients", RowPK: 4, PKColumn: "id", RefID: "c1", InsertionOrder: 0},
		}

		modified := []*domain.EntityFile{
			{
				Path:        "client.yaml",
				ContentHash: "newhash",
				Entities: []domain.Entity{
					{Table: "clients", RefID: "c1", PKColumn: "id", Columns: map[string]any{"name": "still here"}},
					{Table: "clients", RefID: "c2", PKColumn: "id", Columns: map[string]any{"name": "brand new"}},
				},
			},
		}

		_, err := (SyncEntitiesAction{DB: db, Modified: modified}).Execute(context.Background())
		if !errors.Is(err, domain.ErrStructuralChange) {
			t.Fatalf("expected ErrStructuralChange, got %v", err)
		}
		if len(db.updatedRows) != 0 {
			t.Errorf("expected no updates after structural-change detection, got %d", len(db.updatedRows))
		}
	})

	t.Run("it reports a structural change on a positional table mismatch", func(t *testing.T) {
		db := newMockDBAdapter()
		db.synced["graph.yaml"] = true
		db.entityRows = []domain.TrackedRow{
			{EntityFile: "graph.yaml", TableName: "users", RowPK: 1, PKColumn: "id", RefID: "u1", InsertionOrder: 0},
		}

		modified := []*domain.EntityFile{
			{
				Path:        "graph.yaml",
				ContentHash: "newhash",
				Entities: []domain.Entity{
					{Table: "accounts", RefID: "u1", PKColumn: "id", Columns: map[string]any{"name": "moved table"}},
				},
			},
		}

		_, err := (SyncEntitiesAction{DB: db, Modified: modified}).Execute(context.Background())
		if !errors.Is(err, domain.ErrStructuralChange) {
			t.Fatalf("expected ErrStructuralChange, got %v", err)
		}
	})

	t.Run("it reports a structural change when a tracked entity was removed from the file", func(t *testing.T) {
		db := newMockDBAdapter()
		db.synced["client.yaml"] = true
		db.entityRows = []domain.TrackedRow{
			{EntityFile: "client.yaml", TableName: "clients", RowPK: 4, PKColumn: "id", RefID: "c1", InsertionOrder: 0},
			{EntityFile: "client.yaml", TableName: "grants", RowPK: 9, PKColumn: "id", RefID: "g1", InsertionOrder: 1},
		}

		modified := []*domain.EntityFile{
			{
				Path:        "client.yaml",
				ContentHash: "newhash",
				Entities: []domain.Entity{
					{Table: "clients", RefID: "c1", PKColumn: "id", Columns: map[string]any{"name": "still here"}},
				},
			},
		}

		_, err := (SyncEntitiesAction{DB: db, Modified: modified}).Execute(context.Background())
		if !errors.Is(err, domain.ErrStructuralChange) {
			t.Fatalf("expected ErrStructuralChange, got %v", err)
		}
	})
}
