package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHashFileContent_ValidFile(t *testing.T) {
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
}

func TestHashFileContent_Deterministic(t *testing.T) {
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
}

func TestHashFileContent_DifferentContent(t *testing.T) {
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
}

func TestHashFileContent_MissingFile(t *testing.T) {
	_, err := HashFileContent("/nonexistent/path/file.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}
