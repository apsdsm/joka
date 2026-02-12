package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/apsdsm/joka/internal/domains/migration/domain"
)

func TestApply_Success(t *testing.T) {
	dir := t.TempDir()
	sqlFile := filepath.Join(dir, "240101000000_test.sql")
	os.WriteFile(sqlFile, []byte("CREATE TABLE test (id INT);"), 0644)

	adapter := &mockDBAdapter{hasMigrationsTable: true}
	err := ApplyAction{
		DB: adapter,
		Migration: domain.Migration{
			MigrationIndex: "240101000000",
			FileFullPath:   sqlFile,
		},
	}.Execute(context.Background())

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestApply_SQLError(t *testing.T) {
	dir := t.TempDir()
	sqlFile := filepath.Join(dir, "240101000000_test.sql")
	os.WriteFile(sqlFile, []byte("INVALID SQL;"), 0644)

	adapter := &mockDBAdapter{
		hasMigrationsTable: true,
		applySQLErr:        fmt.Errorf("syntax error"),
	}
	err := ApplyAction{
		DB: adapter,
		Migration: domain.Migration{
			MigrationIndex: "240101000000",
			FileFullPath:   sqlFile,
		},
	}.Execute(context.Background())

	if err == nil {
		t.Fatal("expected error for SQL failure")
	}
}

func TestApply_RecordError(t *testing.T) {
	dir := t.TempDir()
	sqlFile := filepath.Join(dir, "240101000000_test.sql")
	os.WriteFile(sqlFile, []byte("SELECT 1;"), 0644)

	adapter := &mockDBAdapter{
		hasMigrationsTable: true,
		recordAppliedErr:   fmt.Errorf("duplicate key"),
	}
	err := ApplyAction{
		DB: adapter,
		Migration: domain.Migration{
			MigrationIndex: "240101000000",
			FileFullPath:   sqlFile,
		},
	}.Execute(context.Background())

	if err == nil {
		t.Fatal("expected error for record failure")
	}
}
