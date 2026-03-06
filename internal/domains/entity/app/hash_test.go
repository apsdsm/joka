package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHashFileContent(t *testing.T) {
	t.Run("it returns a 64-char hex string for a valid file", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.yaml")
		os.WriteFile(path, []byte("hello world"), 0644)

		hash, err := HashFileContent(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(hash) != 64 {
			t.Errorf("expected 64-char hex string, got %d chars: %s", len(hash), hash)
		}
	})

	t.Run("it produces the same hash for the same content", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.yaml")
		os.WriteFile(path, []byte("same content"), 0644)

		hash1, err := HashFileContent(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		hash2, err := HashFileContent(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if hash1 != hash2 {
			t.Errorf("expected same hash, got %s and %s", hash1, hash2)
		}
	})

	t.Run("it produces different hashes for different content", func(t *testing.T) {
		dir := t.TempDir()
		path1 := filepath.Join(dir, "a.yaml")
		path2 := filepath.Join(dir, "b.yaml")
		os.WriteFile(path1, []byte("content a"), 0644)
		os.WriteFile(path2, []byte("content b"), 0644)

		hash1, err := HashFileContent(path1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		hash2, err := HashFileContent(path2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if hash1 == hash2 {
			t.Errorf("expected different hashes for different content, both got %s", hash1)
		}
	})

	t.Run("it returns an error for a missing file", func(t *testing.T) {
		_, err := HashFileContent("/nonexistent/path/file.yaml")
		if err == nil {
			t.Fatal("expected error for missing file, got nil")
		}
	})
}
