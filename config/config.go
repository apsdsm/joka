package config

import (
	"fmt"
	"os"

	"github.com/apsdsm/joka/internal/domains/template/domain"
	"gopkg.in/yaml.v3"
)

type TableConfig struct {
	Name     string              `yaml:"name"`
	Strategy domain.StrategyType `yaml:"strategy"`
}

// Secret describes where to pull secrets from: either connection secrets when
// a Connection's source is a secret provider (e.g. aws_secrets_manager), or a
// named source in the top-level `secrets:` map referenced by entity templates
// as {{ asm.<source>.<key> }} (only secret_id/region apply there).
//
// Two modes:
//   - whole-URL: the secret holds a complete DSN. Set url_key to the JSON key
//     that holds it, or store the secret as a plain (non-JSON) string.
//   - assembly: the secret holds just the password (password_key); host/port/
//     user/database come from the Connection. joka builds a URL-safe DSN.
type Secret struct {
	SecretID    string `yaml:"secret_id"`
	Region      string `yaml:"region"`
	URLKey      string `yaml:"url_key"`
	PasswordKey string `yaml:"password_key"`
}

// Connection describes how joka obtains its database DSN.
//
// Source is usually inferred and can be omitted:
//   - a `secret` block      -> "aws_secrets_manager"
//   - a `url` or `password` -> "literal" (connection data is in the file itself)
//   - otherwise             -> "env" (DATABASE_URL)
//
// `url` / `password` are plaintext in the config; only use them for local /
// non-sensitive databases.
type Connection struct {
	Source   string            `yaml:"source"` // "env" | "literal" | "aws_secrets_manager"
	Driver   string            `yaml:"driver"` // "mysql" (default) | "postgres"
	Host     string            `yaml:"host"`
	Port     int               `yaml:"port"`
	User     string            `yaml:"user"`
	Database string            `yaml:"database"`
	Password string            `yaml:"password"` // literal source: inline password
	URL      string            `yaml:"url"`      // literal source: full DSN, used verbatim
	Params   map[string]string `yaml:"params"`
	Secret   *Secret           `yaml:"secret"`
}

// Profile overlays the base config. Set (non-nil) fields override the base;
// unset fields inherit it.
type Profile struct {
	Migrations        *string           `yaml:"migrations"`
	Templates         *string           `yaml:"templates"`
	Entities          *string           `yaml:"entities"`
	Tables            []TableConfig     `yaml:"tables"`
	IgnoreForeignKeys *bool             `yaml:"ignore_foreign_keys"`
	Connection        *Connection       `yaml:"connection"`
	Secrets           map[string]Secret `yaml:"secrets"`
}

type Config struct {
	Migrations        string             `yaml:"migrations"`
	Templates         string             `yaml:"templates"`
	Entities          string             `yaml:"entities"`
	Tables            []TableConfig      `yaml:"tables"`
	IgnoreForeignKeys bool               `yaml:"ignore_foreign_keys"`
	Connection        *Connection        `yaml:"connection"`
	Secrets           map[string]Secret  `yaml:"secrets"`
	Profiles          map[string]Profile `yaml:"profiles"`
}

// Load reads .jokarc.yaml from the current working directory. If the file does
// not exist it returns a zero-value Config (not an error), unless a profile was
// requested. When profile is non-empty, the named profile from the `profiles:`
// map is overlaid on the base config; an unknown profile is an error.
func Load(profile string) (*Config, error) {
	data, err := os.ReadFile(".jokarc.yaml")
	if err != nil {
		if os.IsNotExist(err) {
			if profile != "" {
				return nil, fmt.Errorf("profile %q requested but .jokarc.yaml not found", profile)
			}
			return &Config{}, nil
		}
		return nil, fmt.Errorf("reading .jokarc.yaml: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing .jokarc.yaml: %w", err)
	}

	if profile == "" {
		return &cfg, nil
	}

	p, ok := cfg.Profiles[profile]
	if !ok {
		return nil, fmt.Errorf("unknown profile %q", profile)
	}

	return applyProfile(&cfg, p), nil
}

// applyProfile returns a copy of base with the profile's set fields overlaid.
// The returned config carries no nested profiles.
func applyProfile(base *Config, p Profile) *Config {
	merged := *base
	merged.Profiles = nil

	if p.Migrations != nil {
		merged.Migrations = *p.Migrations
	}
	if p.Templates != nil {
		merged.Templates = *p.Templates
	}
	if p.Entities != nil {
		merged.Entities = *p.Entities
	}
	if p.Tables != nil {
		merged.Tables = p.Tables
	}
	if p.IgnoreForeignKeys != nil {
		merged.IgnoreForeignKeys = *p.IgnoreForeignKeys
	}
	if p.Connection != nil {
		merged.Connection = p.Connection
	}
	if len(p.Secrets) > 0 {
		sources := make(map[string]Secret, len(base.Secrets)+len(p.Secrets))
		for name, s := range base.Secrets {
			sources[name] = s
		}
		for name, s := range p.Secrets {
			sources[name] = s
		}
		merged.Secrets = sources
	}

	return &merged
}
