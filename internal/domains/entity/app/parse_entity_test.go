package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseEntityAction_BasicEntity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")

	yaml := `entities:
  - _is: users
    _id: admin
    name: Admin
    email: admin@test.com
`
	os.WriteFile(path, []byte(yaml), 0644)

	file, err := ParseEntityAction{Path: path}.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(file.Entities) != 1 {
		t.Fatalf("expected 1 entity, got %d", len(file.Entities))
	}

	e := file.Entities[0]

	if e.Table != "users" {
		t.Errorf("expected table 'users', got %q", e.Table)
	}

	if e.RefID != "admin" {
		t.Errorf("expected refID 'admin', got %q", e.RefID)
	}

	if e.Columns["name"] != "Admin" {
		t.Errorf("expected name 'Admin', got %v", e.Columns["name"])
	}

	if e.Columns["email"] != "admin@test.com" {
		t.Errorf("expected email 'admin@test.com', got %v", e.Columns["email"])
	}
}

func TestParseEntityAction_WithChildren(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")

	yaml := `entities:
  - _is: persons
    _id: test_person
    name: Test
    _has:
      - _is: identities
        _id: test_identity
        person_id: "{{ test_person.id }}"
        type: email
`
	os.WriteFile(path, []byte(yaml), 0644)

	file, err := ParseEntityAction{Path: path}.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(file.Entities) != 1 {
		t.Fatalf("expected 1 root entity, got %d", len(file.Entities))
	}

	parent := file.Entities[0]

	if parent.Table != "persons" {
		t.Errorf("expected parent table 'persons', got %q", parent.Table)
	}

	if len(parent.Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(parent.Children))
	}

	child := parent.Children[0]

	if child.Table != "identities" {
		t.Errorf("expected child table 'identities', got %q", child.Table)
	}

	if child.RefID != "test_identity" {
		t.Errorf("expected child refID 'test_identity', got %q", child.RefID)
	}

	if child.Columns["person_id"] != "{{ test_person.id }}" {
		t.Errorf("expected template expression, got %v", child.Columns["person_id"])
	}
}

func TestParseEntityAction_MultipleEntities(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")

	yaml := `entities:
  - _is: users
    name: Alice
  - _is: users
    name: Bob
`
	os.WriteFile(path, []byte(yaml), 0644)

	file, err := ParseEntityAction{Path: path}.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(file.Entities) != 2 {
		t.Fatalf("expected 2 entities, got %d", len(file.Entities))
	}
}

func TestParseEntityAction_MissingIs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")

	yaml := `entities:
  - name: Alice
`
	os.WriteFile(path, []byte(yaml), 0644)

	_, err := ParseEntityAction{Path: path}.Execute()
	if err == nil {
		t.Fatal("expected error for missing _is, got nil")
	}
}

func TestParseEntityAction_NoIdField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")

	yaml := `entities:
  - _is: settings
    key: theme
    value: dark
`
	os.WriteFile(path, []byte(yaml), 0644)

	file, err := ParseEntityAction{Path: path}.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	e := file.Entities[0]

	if e.RefID != "" {
		t.Errorf("expected empty refID, got %q", e.RefID)
	}

	if e.Columns["key"] != "theme" {
		t.Errorf("expected key 'theme', got %v", e.Columns["key"])
	}
}

func TestParseEntityAction_NonStringValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.yaml")

	yaml := `entities:
  - _is: settings
    count: 42
    active: true
`
	os.WriteFile(path, []byte(yaml), 0644)

	file, err := ParseEntityAction{Path: path}.Execute()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	e := file.Entities[0]

	if e.Columns["count"] != 42 {
		t.Errorf("expected count 42, got %v (type %T)", e.Columns["count"], e.Columns["count"])
	}

	if e.Columns["active"] != true {
		t.Errorf("expected active true, got %v (type %T)", e.Columns["active"], e.Columns["active"])
	}
}
