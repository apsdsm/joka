package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/apsdsm/joka/internal/domains/migration/domain"
)

func TestApply(t *testing.T) {
	t.Run("it applies a valid SQL file successfully", func(t *testing.T) {
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
	})

	t.Run("it returns an error when SQL execution fails", func(t *testing.T) {
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
	})

	t.Run("it returns an error when recording the migration fails", func(t *testing.T) {
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
	})
}
