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

		cfg, err := Load("")
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

		cfg, err := Load("")
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

		_, err := Load("")
		if err == nil {
			t.Fatal("expected error for invalid YAML, got nil")
		}
	})
}

func TestLoadProfile(t *testing.T) {
	const cfgYAML = `migrations: db/migrations
entities: db/entities
connection:
  source: env
profiles:
  local:
    connection:
      source: env
  dev-remote:
    entities: db/entities-dev
    connection:
      source: aws_secrets_manager
      driver: mysql
      host: 127.0.0.1
      port: 3307
      user: root
      database: lgc
      secret:
        secret_id: lgc
        region: ap-northeast-1
        password_key: mysql_root_password
`

	writeCfg := func(t *testing.T) {
		t.Helper()
		dir := t.TempDir()
		orig, _ := os.Getwd()
		os.Chdir(dir)
		t.Cleanup(func() { os.Chdir(orig) })
		if err := os.WriteFile(".jokarc.yaml", []byte(cfgYAML), 0644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("no profile returns the base config", func(t *testing.T) {
		writeCfg(t)
		cfg, err := Load("")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if cfg.Entities != "db/entities" {
			t.Errorf("expected base entities, got %q", cfg.Entities)
		}
		if cfg.Connection == nil || cfg.Connection.Source != "env" {
			t.Errorf("expected base env connection, got %+v", cfg.Connection)
		}
		if cfg.Profiles == nil {
			t.Error("expected base config to retain profiles map")
		}
	})

	t.Run("profile overlays connection and inherits base fields", func(t *testing.T) {
		writeCfg(t)
		cfg, err := Load("dev-remote")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// inherited from base
		if cfg.Migrations != "db/migrations" {
			t.Errorf("expected inherited migrations, got %q", cfg.Migrations)
		}
		// overridden by profile
		if cfg.Entities != "db/entities-dev" {
			t.Errorf("expected overridden entities, got %q", cfg.Entities)
		}
		if cfg.Connection == nil || cfg.Connection.Source != "aws_secrets_manager" {
			t.Fatalf("expected aws connection, got %+v", cfg.Connection)
		}
		if cfg.Connection.Secret == nil || cfg.Connection.Secret.PasswordKey != "mysql_root_password" {
			t.Errorf("unexpected secret config: %+v", cfg.Connection.Secret)
		}
		if cfg.Profiles != nil {
			t.Error("resolved profile config should not carry nested profiles")
		}
	})

	t.Run("unknown profile errors", func(t *testing.T) {
		writeCfg(t)
		if _, err := Load("nope"); err == nil {
			t.Fatal("expected error for unknown profile")
		}
	})
}
