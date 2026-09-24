package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestPathListUnmarshal(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"a bare path, which is what almost every project has", "entities: ./seeds", []string{"./seeds"}},
		{"a list", "entities: [../shared, ./seeds]", []string{"../shared", "./seeds"}},
		{"a block list", "entities:\n  - ../shared\n  - ./seeds", []string{"../shared", "./seeds"}},
		{"absent", "migrations: ./m", nil},
		{"an empty string is no path, not one empty path", `entities: ""`, nil},
		{"an empty list", "entities: []", []string{}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var cfg Config
			if err := yaml.Unmarshal([]byte(c.in), &cfg); err != nil {
				t.Fatalf("unmarshalling: %v", err)
			}
			if len(cfg.Entities) != len(c.want) {
				t.Fatalf("expected %v, got %v", c.want, cfg.Entities)
			}
			for i, want := range c.want {
				if cfg.Entities[i] != want {
					t.Errorf("expected %v, got %v", c.want, cfg.Entities)
				}
			}
		})
	}

	t.Run("anything else is refused with the line number", func(t *testing.T) {
		var cfg Config
		err := yaml.Unmarshal([]byte("entities:\n  a: b\n"), &cfg)
		if err == nil {
			t.Fatal("expected a mapping to be refused")
		}
	})
}
