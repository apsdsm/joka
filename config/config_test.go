package config

import (
	"os"
	"testing"

	"github.com/apsdsm/joka/internal/domains/template/domain"
)

func TestLoad(t *testing.T) {
	t.Run("it parses a valid YAML config", func(t *testing.T) {
		dir := t.TempDir()
		orig, _ := os.Getwd()
		os.Chdir(dir)
		defer os.Chdir(orig)

		yaml := `migrations: db/migrations
templates: db/templates
tables:
  - name: emails
    strategy: truncate
  - name: settings
    strategy: update
`
		os.WriteFile(".jokarc.yaml", []byte(yaml), 0644)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Migrations != "db/migrations" {
			t.Errorf("expected migrations 'db/migrations', got %q", cfg.Migrations)
		}
		if cfg.Templates != "db/templates" {
			t.Errorf("expected templates 'db/templates', got %q", cfg.Templates)
		}
		if len(cfg.Tables) != 2 {
			t.Fatalf("expected 2 tables, got %d", len(cfg.Tables))
		}
		if cfg.Tables[0].Name != "emails" || cfg.Tables[0].Strategy != domain.StrategyTruncate {
			t.Errorf("unexpected first table: %+v", cfg.Tables[0])
		}
		if cfg.Tables[1].Name != "settings" || cfg.Tables[1].Strategy != domain.StrategyUpdate {
			t.Errorf("unexpected second table: %+v", cfg.Tables[1])
		}
	})

	t.Run("it returns zero-value config when file is missing", func(t *testing.T) {
		dir := t.TempDir()
		orig, _ := os.Getwd()
		os.Chdir(dir)
		defer os.Chdir(orig)

		cfg, err := Load()
		if err != nil {
			t.Fatalf("expected no error for missing file, got: %v", err)
		}
		if cfg.Migrations != "" || cfg.Templates != "" || len(cfg.Tables) != 0 {
			t.Errorf("expected zero-value config, got %+v", cfg)
		}
	})

	t.Run("it returns an error for invalid YAML", func(t *testing.T) {
		dir := t.TempDir()
		orig, _ := os.Getwd()
		os.Chdir(dir)
		defer os.Chdir(orig)

		os.WriteFile(".jokarc.yaml", []byte("{{invalid yaml"), 0644)

		_, err := Load()
		if err == nil {
			t.Fatal("expected error for invalid YAML, got nil")
		}
	})
}
