package infra

import (
	"os"
	"path/filepath"
	"testing"
)

func TestListMigrationFiles_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	files, err := ListMigrationFiles(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("expected 0 files, got %d", len(files))
	}
}

func TestListMigrationFiles_ValidFiles(t *testing.T) {
	dir := t.TempDir()
	names := []string{
		"240101120000_create_users.sql",
		"240102120000_add_email.sql",
	}
	for _, name := range names {
		os.WriteFile(filepath.Join(dir, name), []byte("SELECT 1;"), 0644)
	}

	files, err := ListMigrationFiles(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}
	if files[0].Index != "240101120000" {
		t.Errorf("expected index 240101120000, got %s", files[0].Index)
	}
	if files[0].Name != "create_users" {
		t.Errorf("expected name create_users, got %s", files[0].Name)
	}
	if files[1].Index != "240102120000" {
		t.Errorf("expected index 240102120000, got %s", files[1].Index)
	}
}

func TestListMigrationFiles_SortedByIndex(t *testing.T) {
	dir := t.TempDir()
	// Write in reverse order
	os.WriteFile(filepath.Join(dir, "240201000000_second.sql"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "240101000000_first.sql"), []byte(""), 0644)

	files, err := ListMigrationFiles(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if files[0].Index != "240101000000" {
		t.Errorf("expected first file index 240101000000, got %s", files[0].Index)
	}
	if files[1].Index != "240201000000" {
		t.Errorf("expected second file index 240201000000, got %s", files[1].Index)
	}
}

func TestListMigrationFiles_IgnoresNonMatching(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "240101120000_valid.sql"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "readme.md"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "short_name.sql"), []byte(""), 0644)

	files, err := ListMigrationFiles(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}
}

func TestListMigrationFiles_MissingDir(t *testing.T) {
	_, err := ListMigrationFiles("/nonexistent/path")
	if err == nil {
		t.Fatal("expected error for missing directory")
	}
}

func TestCreateMigrationFile(t *testing.T) {
	dir := t.TempDir()
	filename, err := CreateMigrationFile(dir, "add_users")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !migrationPattern.MatchString(filename) {
		t.Errorf("filename %q does not match migration pattern", filename)
	}

	// Verify file exists on disk
	fullPath := filepath.Join(dir, filename)
	if _, err := os.Stat(fullPath); err != nil {
		t.Errorf("migration file not found on disk: %v", err)
	}
}

func TestCreateMigrationFile_MissingDir(t *testing.T) {
	_, err := CreateMigrationFile("/nonexistent/dir", "test")
	if err == nil {
		t.Fatal("expected error for missing directory")
	}
}
