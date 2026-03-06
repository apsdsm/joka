package infra

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverEntityFiles(t *testing.T) {
	t.Run("it finds YAML files in nested directories", func(t *testing.T) {
		dir := t.TempDir()

		personsDir := filepath.Join(dir, "persons")
		os.Mkdir(personsDir, 0755)
		os.WriteFile(filepath.Join(personsDir, "test_person.yaml"), []byte("entities: []"), 0644)

		clientsDir := filepath.Join(dir, "clients")
		os.Mkdir(clientsDir, 0755)
		os.WriteFile(filepath.Join(clientsDir, "test_client.yml"), []byte("entities: []"), 0644)

		files, err := DiscoverEntityFiles(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(files) != 2 {
			t.Fatalf("expected 2 files, got %d: %v", len(files), files)
		}

		expected := map[string]bool{
			filepath.Join("clients", "test_client.yml"):  true,
			filepath.Join("persons", "test_person.yaml"): true,
		}

		for _, f := range files {
			if !expected[f] {
				t.Errorf("unexpected file: %q", f)
			}
		}
	})

	t.Run("it ignores non-YAML files", func(t *testing.T) {
		dir := t.TempDir()

		os.WriteFile(filepath.Join(dir, "readme.md"), []byte("# readme"), 0644)
		os.WriteFile(filepath.Join(dir, "data.json"), []byte("{}"), 0644)
		os.WriteFile(filepath.Join(dir, "valid.yaml"), []byte("entities: []"), 0644)

		files, err := DiscoverEntityFiles(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(files) != 1 {
			t.Fatalf("expected 1 file, got %d: %v", len(files), files)
		}

		if files[0] != "valid.yaml" {
			t.Errorf("expected 'valid.yaml', got %q", files[0])
		}
	})

	t.Run("it returns an empty slice for an empty directory", func(t *testing.T) {
		dir := t.TempDir()

		files, err := DiscoverEntityFiles(dir)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(files) != 0 {
			t.Errorf("expected 0 files, got %d", len(files))
		}
	})

	t.Run("it returns an error for a missing directory", func(t *testing.T) {
		_, err := DiscoverEntityFiles("/nonexistent/path")
		if err == nil {
			t.Fatal("expected error for missing directory, got nil")
		}
	})
}
