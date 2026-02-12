package app

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/apsdsm/joka/internal/domains/migration/domain"
	"github.com/apsdsm/joka/internal/domains/migration/infra/models"
)

type mockDBAdapter struct {
	hasMigrationsTable    bool
	hasMigrationsTableErr error
	appliedMigrations     []models.MigrationRow
	appliedMigrationsErr  error
	applySQLErr           error
	recordAppliedErr      error
	createTableErr        error
}

func (m *mockDBAdapter) HasMigrationsTable(ctx context.Context) (bool, error) {
	return m.hasMigrationsTable, m.hasMigrationsTableErr
}

func (m *mockDBAdapter) CreateMigrationsTable(ctx context.Context) error {
	return m.createTableErr
}

func (m *mockDBAdapter) GetAppliedMigrations(ctx context.Context) ([]models.MigrationRow, error) {
	return m.appliedMigrations, m.appliedMigrationsErr
}

func (m *mockDBAdapter) ApplySQLFromFile(ctx context.Context, filePath string) error {
	return m.applySQLErr
}

func (m *mockDBAdapter) RecordMigrationApplied(ctx context.Context, migrationIndex string) error {
	return m.recordAppliedErr
}

func (m *mockDBAdapter) EnsureSnapshotsTable(ctx context.Context) error { return nil }
func (m *mockDBAdapter) CaptureSchemaSnapshot(ctx context.Context, migrationIndex string) error {
	return nil
}
func (m *mockDBAdapter) GetSchemaSnapshot(ctx context.Context, migrationIndex string) (string, error) {
	return "", nil
}
func (m *mockDBAdapter) GetLatestSnapshotIndex(ctx context.Context) (string, error) {
	return "", nil
}

func createTestFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("SELECT 1;"), 0644); err != nil {
		t.Fatalf("creating test file: %v", err)
	}
}

func TestGetMigrationChain_AllApplied(t *testing.T) {
	dir := t.TempDir()
	createTestFile(t, dir, "240101000000_first.sql")
	createTestFile(t, dir, "240102000000_second.sql")

	adapter := &mockDBAdapter{
		hasMigrationsTable: true,
		appliedMigrations: []models.MigrationRow{
			{ID: 1, MigrationIndex: "240101000000", AppliedAt: time.Now()},
			{ID: 2, MigrationIndex: "240102000000", AppliedAt: time.Now()},
		},
	}

	chain, err := GetMigrationChainAction{DB: adapter, MigrationsDir: dir}.Execute(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("expected 2 migrations, got %d", len(chain))
	}
	for _, m := range chain {
		if m.Status != domain.StatusApplied {
			t.Errorf("expected status applied, got %s", m.Status)
		}
	}
}

func TestGetMigrationChain_AllPending(t *testing.T) {
	dir := t.TempDir()
	createTestFile(t, dir, "240101000000_first.sql")
	createTestFile(t, dir, "240102000000_second.sql")

	adapter := &mockDBAdapter{
		hasMigrationsTable: true,
		appliedMigrations:  nil,
	}

	chain, err := GetMigrationChainAction{DB: adapter, MigrationsDir: dir}.Execute(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("expected 2 migrations, got %d", len(chain))
	}
	for _, m := range chain {
		if m.Status != domain.StatusPending {
			t.Errorf("expected status pending, got %s", m.Status)
		}
	}
}

func TestGetMigrationChain_Mixed(t *testing.T) {
	dir := t.TempDir()
	createTestFile(t, dir, "240101000000_first.sql")
	createTestFile(t, dir, "240102000000_second.sql")

	adapter := &mockDBAdapter{
		hasMigrationsTable: true,
		appliedMigrations: []models.MigrationRow{
			{ID: 1, MigrationIndex: "240101000000", AppliedAt: time.Now()},
		},
	}

	chain, err := GetMigrationChainAction{DB: adapter, MigrationsDir: dir}.Execute(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("expected 2 migrations, got %d", len(chain))
	}
	if chain[0].Status != domain.StatusApplied {
		t.Errorf("expected first migration applied, got %s", chain[0].Status)
	}
	if chain[1].Status != domain.StatusPending {
		t.Errorf("expected second migration pending, got %s", chain[1].Status)
	}
}

func TestGetMigrationChain_BrokenChain(t *testing.T) {
	dir := t.TempDir()
	createTestFile(t, dir, "240101000000_first.sql")
	createTestFile(t, dir, "240102000000_second.sql")

	adapter := &mockDBAdapter{
		hasMigrationsTable: true,
		appliedMigrations: []models.MigrationRow{
			{ID: 1, MigrationIndex: "999999999999", AppliedAt: time.Now()},
		},
	}

	_, err := GetMigrationChainAction{DB: adapter, MigrationsDir: dir}.Execute(context.Background())
	if err == nil {
		t.Fatal("expected error for broken chain")
	}
}

func TestGetMigrationChain_MissingFile(t *testing.T) {
	dir := t.TempDir()
	adapter := &mockDBAdapter{
		hasMigrationsTable: true,
		appliedMigrations: []models.MigrationRow{
			{ID: 1, MigrationIndex: "240101000000", AppliedAt: time.Now()},
		},
	}

	_, err := GetMigrationChainAction{DB: adapter, MigrationsDir: dir}.Execute(context.Background())
	if err == nil {
		t.Fatal("expected error for missing migration file")
	}
}

func TestGetMigrationChain_Empty(t *testing.T) {
	dir := t.TempDir()
	adapter := &mockDBAdapter{
		hasMigrationsTable: true,
		appliedMigrations:  nil,
	}

	chain, err := GetMigrationChainAction{DB: adapter, MigrationsDir: dir}.Execute(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(chain) != 0 {
		t.Fatalf("expected 0 migrations, got %d", len(chain))
	}
}
