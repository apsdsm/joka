package config

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// TunnelTarget is what a tunnel connects through: a literal instance id, or a
// selector that finds one.
//
//	target: i-0123456789abcdef0
//	target: { tag: Role=relay }
//	target: { tags: { Role: relay, Env: test } }
//
// The selector exists because the relays are in auto scaling groups, so the id
// changes on every instance refresh and a committed one goes stale. A config
// that has to be edited whenever the infrastructure replaces a machine is a
// config that will be wrong when someone needs it.
type TunnelTarget struct {
	// ID is a literal instance id, when one was given.
	ID string
	// Tags selects an instance. Every tag must match.
	Tags map[string]string
}

// UnmarshalYAML accepts a scalar id or a selector mapping.
func (t *TunnelTarget) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		return value.Decode(&t.ID)

	case yaml.MappingNode:
		var raw struct {
			Tag  string            `yaml:"tag"`
			Tags map[string]string `yaml:"tags"`
		}
		if err := value.Decode(&raw); err != nil {
			return err
		}

		t.Tags = map[string]string{}
		for k, v := range raw.Tags {
			t.Tags[k] = v
		}

		if raw.Tag != "" {
			key, val, ok := strings.Cut(raw.Tag, "=")
			if !ok || key == "" {
				return fmt.Errorf("line %d: tag must be written Key=Value, got %q", value.Line, raw.Tag)
			}
			t.Tags[strings.TrimSpace(key)] = strings.TrimSpace(val)
		}

		if len(t.Tags) == 0 {
			return fmt.Errorf("line %d: a tunnel target selector needs `tag:` or `tags:`", value.Line)
		}

		return nil
	}

	return fmt.Errorf("line %d: expected an instance id or a selector", value.Line)
}

// IsZero keeps an unset target out of a marshalled config.
func (t TunnelTarget) IsZero() bool { return t.ID == "" && len(t.Tags) == 0 }

// Describe renders the target for an error message.
func (t TunnelTarget) Describe() string {
	if t.ID != "" {
		return t.ID
	}
	if len(t.Tags) == 0 {
		return "(no target)"
	}

	pairs := make([]string, 0, len(t.Tags))
	for k, v := range t.Tags {
		pairs = append(pairs, k+"="+v)
	}
	sort.Strings(pairs)

	return strings.Join(pairs, ",")
}
