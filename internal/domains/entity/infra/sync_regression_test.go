package infra_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/app"
	"github.com/apsdsm/joka/internal/domains/entity/domain"
	"github.com/apsdsm/joka/internal/domains/entity/infra"
	"github.com/apsdsm/joka/testlib"
)

// syncEntityFile mirrors what `entity sync` does for a single file: parse the
// YAML at fullPath, attach the relative path and current content hash, then
// insert its entity graph (and tracking rows) through SyncEntitiesAction in a
// transaction against the real adapter. It returns nothing; failures fail t.
func syncEntityFile(t *testing.T, db *sql.DB, fullPath, rel string) {
	t.Helper()
	ctx := context.Background()

	hash, err := app.HashFileContent(fullPath)
	if err != nil {
		t.Fatalf("hashing %s: %v", rel, err)
	}

	file, err := app.ParseEntityAction{Path: fullPath}.Execute()
	if err != nil {
		t.Fatalf("parsing %s: %v", rel, err)
	}
	file.Path = rel
	file.ContentHash = hash

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	txAdapter := infra.NewPostgresTxDBAdapter(tx, db)

	if _, err := (app.SyncEntitiesAction{DB: txAdapter, Files: []*domain.EntityFile{file}}).Execute(ctx); err != nil {
		tx.Rollback() //nolint:errcheck
		t.Fatalf("initial sync of %s: %v", rel, err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit initial sync: %v", err)
	}
}

// classify mirrors `entity status`: it asks EntityStatusAction for the status
// of rel given the file on disk and the recorded tracking state.
func classify(t *testing.T, db *sql.DB, entitiesDir, rel string) domain.FileStatus {
	t.Helper()
	ctx := context.Background()

	results, err := (app.EntityStatusAction{
		DB:          infra.NewPostgresDBAdapter(db),
		EntitiesDir: entitiesDir,
		Files:       []string{rel},
	}).Execute(ctx)
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	for _, r := range results {
		if r.Path == rel {
			return r.Status
		}
	}
	t.Fatalf("no status reported for %s", rel)
	return ""
}

// applyModified mirrors the sync command's update path for a single modified
// file: re-parse, re-hash, and run SyncEntitiesAction with the file in the
// Modified slice so its tracked rows are UPDATEd in place.
func applyModified(t *testing.T, db *sql.DB, fullPath, rel string) {
	t.Helper()
	ctx := context.Background()

	hash, err := app.HashFileContent(fullPath)
	if err != nil {
		t.Fatalf("re-hashing %s: %v", rel, err)
	}

	file, err := app.ParseEntityAction{Path: fullPath}.Execute()
	if err != nil {
		t.Fatalf("re-parsing %s: %v", rel, err)
	}
	file.Path = rel
	file.ContentHash = hash

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin tx: %v", err)
	}

	txAdapter := infra.NewPostgresTxDBAdapter(tx, db)

	if _, err := (app.SyncEntitiesAction{DB: txAdapter, Modified: []*domain.EntityFile{file}}).Execute(ctx); err != nil {
		tx.Rollback() //nolint:errcheck
		t.Fatalf("update sync of %s: %v", rel, err)
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit update sync: %v", err)
	}
}

// TestPostgresEntitySyncDetectsModifiedColumnValue is a regression test for the
// reported "already synced" miss: it syncs a seed file, edits a single column
// value (no structural change — same entities/tables/_ids), and verifies the
// status path classifies the file as Modified and the re-sync actually lands
// the new column value in the database.
func TestPostgresEntitySyncDetectsModifiedColumnValue(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	const table = "test_entity_modcol_users"
	createPostgresTestTable(t, db, table)
	createPostgresEntityTrackingTables(t, db)

	dir := t.TempDir()
	rel := "users.yaml"
	fullPath := filepath.Join(dir, rel)

	const before = `entities:
  - _is: test_entity_modcol_users
    _id: alice
    name: Alice
    email: alice@before.test
`
	if err := os.WriteFile(fullPath, []byte(before), 0o600); err != nil {
		t.Fatalf("writing seed file: %v", err)
	}

	ctx := context.Background()
	adapter := infra.NewPostgresDBAdapter(db)

	t.Run("it reports synced immediately after the initial sync", func(t *testing.T) {
		syncEntityFile(t, db, fullPath, rel)

		if got := classify(t, db, dir, rel); got != domain.StatusSynced {
			t.Fatalf("expected %q right after sync, got %q", domain.StatusSynced, got)
		}

		var email string
		if err := db.QueryRowContext(ctx, `SELECT email FROM "`+table+`" WHERE name = $1`, "Alice").Scan(&email); err != nil {
			t.Fatalf("querying inserted row: %v", err)
		}
		if email != "alice@before.test" {
			t.Fatalf("expected initial email alice@before.test, got %q", email)
		}
	})

	t.Run("it detects a changed column value as modified and applies the update", func(t *testing.T) {
		// Change only a column value: same entity, same table, same _id —
		// no structural change. This is exactly the scenario that was
		// reportedly missed.
		const after = `entities:
  - _is: test_entity_modcol_users
    _id: alice
    name: Alice
    email: alice@after.test
`
		if err := os.WriteFile(fullPath, []byte(after), 0o600); err != nil {
			t.Fatalf("rewriting seed file: %v", err)
		}

		// Sanity: the on-disk hash must differ from the stored one, otherwise
		// the test isn't exercising change detection at all.
		newHash, err := app.HashFileContent(fullPath)
		if err != nil {
			t.Fatalf("hashing edited file: %v", err)
		}
		storedHash, err := adapter.GetEntityHash(ctx, rel)
		if err != nil {
			t.Fatalf("GetEntityHash: %v", err)
		}
		if newHash == storedHash {
			t.Fatalf("edited file hash matches stored hash %q — edit did not change content", storedHash)
		}

		if got := classify(t, db, dir, rel); got != domain.StatusModified {
			t.Fatalf("expected %q after editing a column value, got %q", domain.StatusModified, got)
		}

		applyModified(t, db, fullPath, rel)

		var email string
		if err := db.QueryRowContext(ctx, `SELECT email FROM "`+table+`" WHERE name = $1`, "Alice").Scan(&email); err != nil {
			t.Fatalf("querying updated row: %v", err)
		}
		if email != "alice@after.test" {
			t.Fatalf("expected updated email alice@after.test to land in DB, got %q", email)
		}

		// After applying, the stored hash should be refreshed and the file
		// should read back as synced.
		if got := classify(t, db, dir, rel); got != domain.StatusSynced {
			t.Fatalf("expected %q after applying the update, got %q", domain.StatusSynced, got)
		}
	})
}
