package models

import "github.com/nickfiggins/joka/internal/domains/template/domain"

type TableConfig struct {
	Name     string              `yaml:"name"`
	Strategy domain.StrategyType `yaml:"strategy"`
}

type TemplatesConfig struct {
	Name   string        `yaml:"name"`
	Tables []TableConfig `yaml:"tables"`
}
