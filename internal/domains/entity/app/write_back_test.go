package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedFile writes a YAML file and returns its path.
func seedFile(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "a.yaml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing the seed file: %v", err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading back: %v", err)
	}
	return string(body)
}

func TestSetEntityColumn(t *testing.T) {
	t.Run("it keeps the comments, the order and the untouched values", func(t *testing.T) {
		// The whole point: these are files a person maintains. A write-back
		// that reformatted them would make the diff unreadable.
		path := seedFile(t, `# The administrator seeded on every environment.
entities:
  - _is: users
    _id: admin
    name: Admin          # shown in the header
    email: admin@example.com
    active: true
`)

		if err := SetEntityColumn(path, "admin", "email", "changed@example.com"); err != nil {
			t.Fatalf("SetEntityColumn: %v", err)
		}

		got := readFile(t, path)

		for _, want := range []string{
			"# The administrator seeded on every environment.",
			"# shown in the header",
			"email: changed@example.com",
			"active: true",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("expected %q in:\n%s", want, got)
			}
		}

		// Key order is the author's, not the encoder's.
		if strings.Index(got, "name:") > strings.Index(got, "email:") {
			t.Errorf("expected the declared order kept:\n%s", got)
		}
	})

	t.Run("it finds an entity nested under _has", func(t *testing.T) {
		path := seedFile(t, `entities:
  - _is: users
    _id: admin
    name: Admin
    _has:
      - _is: profiles
        _id: admin_profile
        bio: old
`)

		if err := SetEntityColumn(path, "admin_profile", "bio", "new"); err != nil {
			t.Fatalf("SetEntityColumn: %v", err)
		}

		if got := readFile(t, path); !strings.Contains(got, "bio: new") {
			t.Errorf("expected the child updated:\n%s", got)
		}
	})

	t.Run("it keeps a quoted string quoted", func(t *testing.T) {
		// A zip code declared as a string must not come back as an integer.
		path := seedFile(t, `entities:
  - _is: places
    _id: head_office
    postcode: "01234"
`)

		if err := SetEntityColumn(path, "head_office", "postcode", "05678"); err != nil {
			t.Fatalf("SetEntityColumn: %v", err)
		}

		if got := readFile(t, path); !strings.Contains(got, `"05678"`) {
			t.Errorf("expected the value still quoted:\n%s", got)
		}
	})

	t.Run("it keeps an integer unquoted", func(t *testing.T) {
		path := seedFile(t, `entities:
  - _is: settings
    _id: limits
    max_users: 10
`)

		if err := SetEntityColumn(path, "limits", "max_users", "25"); err != nil {
			t.Fatalf("SetEntityColumn: %v", err)
		}

		if got := readFile(t, path); !strings.Contains(got, "max_users: 25") {
			t.Errorf("expected an unquoted integer:\n%s", got)
		}
	})

	t.Run("it retypes when the new value no longer fits", func(t *testing.T) {
		path := seedFile(t, `entities:
  - _is: settings
    _id: limits
    max_users: 10
`)

		if err := SetEntityColumn(path, "limits", "max_users", "unlimited"); err != nil {
			t.Fatalf("SetEntityColumn: %v", err)
		}

		if got := readFile(t, path); !strings.Contains(got, "max_users: unlimited") {
			t.Errorf("expected the value written as a string:\n%s", got)
		}
	})

	t.Run("it writes a null", func(t *testing.T) {
		path := seedFile(t, `entities:
  - _is: users
    _id: admin
    nickname: Ad
`)

		if err := SetEntityColumn(path, "admin", "nickname", "NULL"); err != nil {
			t.Fatalf("SetEntityColumn: %v", err)
		}

		if got := readFile(t, path); !strings.Contains(got, "nickname: null") {
			t.Errorf("expected a null:\n%s", got)
		}
	})

	t.Run("it refuses to overwrite a template", func(t *testing.T) {
		// Writing a literal over an expression would replace the indirection
		// with whatever it happened to resolve to this time, and nothing would
		// say so.
		path := seedFile(t, `entities:
  - _is: users
    _id: admin
    password_hash: "{{ argon2id|admin123 }}"
`)
		before := readFile(t, path)

		err := SetEntityColumn(path, "admin", "password_hash", "$argon2id$whatever")
		if !errors.Is(err, ErrNotWritable) {
			t.Fatalf("expected ErrNotWritable, got %v", err)
		}
		if readFile(t, path) != before {
			t.Error("expected the file untouched after a refusal")
		}
	})

	t.Run("it reports an _id or column it cannot find", func(t *testing.T) {
		path := seedFile(t, `entities:
  - _is: users
    _id: admin
    name: Admin
`)

		if err := SetEntityColumn(path, "nobody", "name", "x"); err == nil {
			t.Error("expected an error for an unknown _id")
		}
		if err := SetEntityColumn(path, "admin", "nickname", "x"); err == nil {
			t.Error("expected an error for an undeclared column")
		}
	})

	t.Run("it keeps the file's mode", func(t *testing.T) {
		path := seedFile(t, `entities:
  - _is: users
    _id: admin
    name: Admin
`)
		if err := os.Chmod(path, 0o640); err != nil {
			t.Fatalf("chmod: %v", err)
		}

		if err := SetEntityColumn(path, "admin", "name", "Renamed"); err != nil {
			t.Fatalf("SetEntityColumn: %v", err)
		}

		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if info.Mode().Perm() != 0o640 {
			t.Errorf("expected mode 0640 kept, got %v", info.Mode().Perm())
		}
	})

	t.Run("the result still parses as the same entity set", func(t *testing.T) {
		path := seedFile(t, `entities:
  - _is: users
    _id: admin
    name: Admin
    _has:
      - _is: profiles
        _id: admin_profile
        bio: old
`)

		if err := SetEntityColumn(path, "admin_profile", "bio", "new"); err != nil {
			t.Fatalf("SetEntityColumn: %v", err)
		}

		file, err := ParseEntityAction{Path: path}.Execute()
		if err != nil {
			t.Fatalf("re-parsing: %v", err)
		}

		if len(file.Entities) != 1 || len(file.Entities[0].Children) != 1 {
			t.Fatalf("expected the graph intact, got %+v", file.Entities)
		}
		if got := file.Entities[0].Children[0].Columns["bio"]; got != "new" {
			t.Errorf("expected bio new, got %v", got)
		}
	})
}

func TestWritable(t *testing.T) {
	t.Run("a literal can be rewritten", func(t *testing.T) {
		if !Writable(ColumnChange{Column: "label", Before: "db", After: "file"}) {
			t.Error("expected a literal writable")
		}
	})

	t.Run("a template cannot", func(t *testing.T) {
		// Writing a literal over an expression would replace the indirection
		// with whatever it resolved to this time.
		if Writable(ColumnChange{Column: "client_id", After: "{{ lookup|clients,id,xid=c1 }}"}) {
			t.Error("expected a template not writable")
		}
	})

	t.Run("a regenerated or deferred column cannot", func(t *testing.T) {
		// There is no value to write, and for a secret there must not be.
		if Writable(ColumnChange{Column: "password_hash", Regenerated: true}) {
			t.Error("expected a regenerated column not writable")
		}
		if Writable(ColumnChange{Column: "client_id", Deferred: true}) {
			t.Error("expected a deferred column not writable")
		}
	})
}

func TestApplyResolutions(t *testing.T) {
	dir := t.TempDir()
	body := `entities:
  - _is: users
    _id: admin
    name: Admin
    email: admin@example.com
`
	if err := os.WriteFile(filepath.Join(dir, "a.yaml"), []byte(body), 0o644); err != nil {
		t.Fatalf("writing the seed file: %v", err)
	}

	changed, err := ApplyResolutions(dir, []Resolution{
		{File: "a.yaml", RefID: "admin", Column: "email", Value: "moved@example.com",
			KeepDatabase: true, UpdateFile: true},
		// Kept but not rewritten: a templated column can have the first
		// without the second.
		{File: "a.yaml", RefID: "admin", Column: "name", KeepDatabase: true},
		// The file won, so nothing is written.
		{File: "a.yaml", RefID: "admin", Column: "nickname"},
	})
	if err != nil {
		t.Fatalf("ApplyResolutions: %v", err)
	}

	if len(changed) != 1 || changed[0] != "a.yaml" {
		t.Errorf("expected a.yaml reported changed, got %v", changed)
	}

	got := readFile(t, filepath.Join(dir, "a.yaml"))
	if !strings.Contains(got, "email: moved@example.com") {
		t.Errorf("expected the email updated:\n%s", got)
	}
	if !strings.Contains(got, "name: Admin") {
		t.Errorf("expected the un-rewritten column untouched:\n%s", got)
	}
}

func TestKeepFromResolutions(t *testing.T) {
	conflicts := []RowConflict{{
		RefID: "admin",
		Columns: []ColumnChange{
			{Column: "email", Before: "moved@example.com", LiveHash: HashValue("moved@example.com")},
			{Column: "name", Before: "Renamed", LiveHash: HashValue("Renamed")},
		},
	}}

	keep := KeepFromResolutions(conflicts, []Resolution{
		{RefID: "admin", Column: "email", KeepDatabase: true},
		{RefID: "admin", Column: "name"},
	})

	if _, held := keep["admin"]["name"]; held {
		t.Error("expected the column the file won left out")
	}
	// Held, with nothing recorded: the declaration is a literal, so it is
	// rewritten to the database's value and the baseline still describes what
	// joka last applied.
	if _, held := keep["admin"]["email"]; !held {
		t.Error("expected the conceded column held")
	}
	if keep["admin"]["email"] != "" {
		t.Errorf("expected no baseline recorded for a rewritable column, got %q", keep["admin"]["email"])
	}

	if got := KeepFromResolutions(conflicts, nil); got != nil {
		t.Errorf("expected nothing kept when nothing was conceded, got %+v", got)
	}
}
