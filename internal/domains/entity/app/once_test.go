package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// parseOnceFile writes a YAML file and parses it.
func parseOnceFile(t *testing.T, body string) (*domain.EntityFile, error) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "a.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the file: %v", err)
	}

	return ParseEntityAction{Path: path}.Execute()
}

func TestParseOnce(t *testing.T) {
	t.Run("it reads the column names and leaves the values in place", func(t *testing.T) {
		file, err := parseOnceFile(t, `entities:
  - _is: users
    _id: admin
    email: admin@example.com
    password_hash: "{{ argon2id|admin123 }}"
    _once:
      - password_hash
`)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		e := file.Entities[0]
		if !e.IsOnce("password_hash") {
			t.Error("expected password_hash marked seed-once")
		}
		if e.IsOnce("email") {
			t.Error("expected email not marked")
		}

		// The value stays with every other column, so a reader sees the whole
		// row in one place and a template in it resolves as normal.
		if e.Columns["password_hash"] != "{{ argon2id|admin123 }}" {
			t.Errorf("expected the value left in Columns, got %v", e.Columns["password_hash"])
		}
		if _, leaked := e.Columns["_once"]; leaked {
			t.Error("expected _once kept out of the columns")
		}
	})

	t.Run("it refuses a name the entity does not declare", func(t *testing.T) {
		_, err := parseOnceFile(t, `entities:
  - _is: users
    _id: admin
    email: admin@example.com
    _once:
      - passwrod_hash
`)
		if err == nil {
			t.Fatal("expected an error for a column that is not declared")
		}
		if !strings.Contains(err.Error(), "passwrod_hash") {
			t.Errorf("expected the offending name in the error, got %v", err)
		}
	})

	t.Run("it refuses a _once that is not a list", func(t *testing.T) {
		_, err := parseOnceFile(t, `entities:
  - _is: users
    _id: admin
    email: admin@example.com
    _once: password_hash
`)
		if err == nil {
			t.Fatal("expected an error for a scalar _once")
		}
	})
}

func TestApplySetLeavesASeededColumnAlone(t *testing.T) {
	db := newMockDBAdapter()

	once := func(path string, password string) *domain.EntityFile {
		e := col("users", "admin", map[string]any{
			"email":         "admin@example.com",
			"password_hash": password,
		})
		e.Once = []string{"password_hash"}
		return entityFile(path, e)
	}

	applyAll(t, db, once("a.yaml", "seeded"))

	if got := db.insertedRows[0].Columns["password_hash"]; got != "seeded" {
		t.Errorf("expected the insert to seed the column, got %v", got)
	}

	// The file changed, so the row is rewritten — but not the column the
	// database owns. This is the password reset that used to revert.
	changed := once("a.yaml", "seeded")
	changed.ContentHash = "hash-changed"
	changed.Entities[0].Columns["email"] = "moved@example.com"
	applyAll(t, db, changed)

	if len(db.updatedRows) != 1 {
		t.Fatalf("expected 1 update, got %d", len(db.updatedRows))
	}
	written := db.updatedRows[0].Columns
	if _, wrote := written["password_hash"]; wrote {
		t.Error("expected password_hash left out of the update")
	}
	if written["email"] != "moved@example.com" {
		t.Errorf("expected the owned column still written, got %v", written["email"])
	}
}

func TestApplySetKeepsASeededColumnsBaseline(t *testing.T) {
	db := newMockDBAdapter()

	build := func(path, email string) *domain.EntityFile {
		e := col("users", "admin", map[string]any{
			"email":         email,
			"password_hash": "seeded",
		})
		e.Once = []string{"password_hash"}
		f := entityFile(path, e)
		f.ContentHash = "hash-" + email
		return f
	}

	applyAll(t, db, build("a.yaml", "first@example.com"))
	applyAll(t, db, build("a.yaml", "second@example.com"))

	admin, ok := db.state.Row("admin")
	if !ok {
		t.Fatal("expected admin tracked")
	}

	// joka applied the column when it inserted the row, and that is still the
	// last thing it applied to it. Dropping the entry on update would lose a
	// fact the baseline exists to hold.
	if hash, recorded := admin.Baseline("password_hash"); !recorded || hash != HashValue("seeded") {
		t.Errorf("expected the seeded column's baseline carried forward, got %q recorded=%v", hash, recorded)
	}
	if hash, _ := admin.Baseline("email"); hash != HashValue("second@example.com") {
		t.Errorf("expected the owned column's baseline updated, got %q", hash)
	}
}

func TestResolveRowChangesSkipsSeededColumns(t *testing.T) {
	db := newMockDBAdapter()
	db.currentRows["users|1"] = map[string]any{
		"email":         "admin@example.com",
		"password_hash": "changed-by-the-app",
	}

	e := col("users", "admin", map[string]any{
		"email":         "admin@example.com",
		"password_hash": "seeded",
	})
	e.Once = []string{"password_hash"}

	row := domain.EntityState{Table: "users", PKColumn: "id", PKValue: 1}

	changes, err := ResolveRowChanges(context.Background(), db, e, row, map[string]int64{}, "now", true, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Sync will not write it, so reporting a change would promise an update
	// that never comes.
	for _, c := range changes {
		if c.Column == "password_hash" {
			t.Errorf("expected the seeded column left out of the changes, got %+v", c)
		}
	}
}
