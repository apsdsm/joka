package config

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

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
	// Provider names the vendor this secret lives with. Empty means "aws",
	// which is what every existing config means and the only one joka ships.
	Provider    string `yaml:"provider"`
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
	Driver   string            `yaml:"driver"` // "postgres" (default); joka is PostgreSQL only
	Host     string            `yaml:"host"`
	Port     int               `yaml:"port"`
	User     string            `yaml:"user"`
	Database string            `yaml:"database"`
	Password string            `yaml:"password"` // literal source: inline password
	URL      string            `yaml:"url"`      // literal source: full DSN, used verbatim
	Params   map[string]string `yaml:"params"`
	Secret   *Secret           `yaml:"secret"`
	// Tunnel forwards a local port to the database when it is not directly
	// reachable. Host and Port are then read from the tunnel's local end.
	Tunnel *Tunnel `yaml:"tunnel"`
}

// Tunnel describes a port forward to open before connecting.
//
// It exists so `cd devops/joka/prod && joka apply` works against a database in
// a private subnet without a wrapper script. Every project that needed one had
// written the same port-forward script, and the script was the only reason most
// of them existed.
type Tunnel struct {
	// Provider names the vendor. Empty means "aws", whose implementation is
	// Session Manager port forwarding.
	Provider string `yaml:"provider"`
	// Target is what to tunnel through — an SSM instance id for aws.
	Target string `yaml:"target"`
	// RemoteHost and RemotePort are the database as the target addresses it.
	// They default to the connection's own host and port, because naming the
	// database twice is how the two come to disagree.
	RemoteHost string `yaml:"remote_host"`
	RemotePort int    `yaml:"remote_port"`
	// LocalPort is the port to listen on. Zero, the default, picks a free one:
	// a fixed port collides with whatever else is running and nothing outside
	// the process needs to predict it.
	LocalPort int `yaml:"local_port"`
	// Params carries anything provider-specific, the same way SecretRef does.
	Params map[string]string `yaml:"params"`
}

// Profile overlays the base config. Set (non-nil) fields override the base;
// unset fields inherit it.
type Profile struct {
	Root       *string           `yaml:"root"`
	Migrations *string           `yaml:"migrations"`
	Entities   *PathList         `yaml:"entities"`
	StateFile  *string           `yaml:"statefile"`
	Connection *Connection       `yaml:"connection"`
	Secrets    map[string]Secret `yaml:"secrets"`
}

type Config struct {
	// Root names this configuration, and through it the database it owns. See
	// meta.KeyStateRoot: a database records the root that claimed it, and a
	// different root is refused rather than allowed to converge it against the
	// wrong desired state.
	Root       string `yaml:"root"`
	Migrations string `yaml:"migrations"`
	// Entities is one seed directory or several, synced as one desired state.
	Entities   PathList           `yaml:"entities"`
	StateFile  string             `yaml:"statefile"`
	Connection *Connection        `yaml:"connection"`
	Secrets    map[string]Secret  `yaml:"secrets"`
	Profiles   map[string]Profile `yaml:"profiles"`
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

	if p.Root != nil {
		merged.Root = *p.Root
	}
	if p.Migrations != nil {
		merged.Migrations = *p.Migrations
	}
	if p.Entities != nil {
		merged.Entities = *p.Entities
	}
	if p.StateFile != nil {
		merged.StateFile = *p.StateFile
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
