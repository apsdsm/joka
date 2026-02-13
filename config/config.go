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

type Config struct {
	Migrations string        `yaml:"migrations"`
	Templates  string        `yaml:"templates"`
	Tables     []TableConfig `yaml:"tables"`
}

// Load reads .jokarc.yaml from the current working directory. If the file does
// not exist, it returns a zero-value Config (not an error).
func Load() (*Config, error) {
	data, err := os.ReadFile(".jokarc.yaml")
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, fmt.Errorf("reading .jokarc.yaml: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing .jokarc.yaml: %w", err)
	}

	return &cfg, nil
}
