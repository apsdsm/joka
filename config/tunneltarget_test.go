package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func parseTunnel(t *testing.T, body string) (*Tunnel, error) {
	t.Helper()

	var cfg Config
	err := yaml.Unmarshal([]byte(body), &cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Connection == nil {
		t.Fatal("expected a connection block")
	}

	return cfg.Connection.Tunnel, nil
}

func TestTunnelTargetUnmarshal(t *testing.T) {
	t.Run("a scalar is an instance id", func(t *testing.T) {
		tun, err := parseTunnel(t, "connection:\n  tunnel:\n    target: i-0123456789abcdef0\n")
		if err != nil {
			t.Fatalf("unmarshalling: %v", err)
		}
		if tun.Target.ID != "i-0123456789abcdef0" || len(tun.Target.Tags) != 0 {
			t.Errorf("expected a literal id, got %+v", tun.Target)
		}
	})

	t.Run("tag: Key=Value selects one", func(t *testing.T) {
		tun, err := parseTunnel(t, "connection:\n  tunnel:\n    target: { tag: Role=relay }\n")
		if err != nil {
			t.Fatalf("unmarshalling: %v", err)
		}
		if tun.Target.ID != "" {
			t.Errorf("expected no literal id, got %q", tun.Target.ID)
		}
		if tun.Target.Tags["Role"] != "relay" {
			t.Errorf("expected Role=relay, got %v", tun.Target.Tags)
		}
	})

	t.Run("tags: takes several", func(t *testing.T) {
		tun, err := parseTunnel(t, "connection:\n  tunnel:\n    target:\n      tags:\n        Role: relay\n        Env: test\n")
		if err != nil {
			t.Fatalf("unmarshalling: %v", err)
		}
		if tun.Target.Tags["Role"] != "relay" || tun.Target.Tags["Env"] != "test" {
			t.Errorf("expected both tags, got %v", tun.Target.Tags)
		}
	})

	t.Run("a tag without = is refused, naming the shape", func(t *testing.T) {
		_, err := parseTunnel(t, "connection:\n  tunnel:\n    target: { tag: Role }\n")
		if err == nil || !strings.Contains(err.Error(), "Key=Value") {
			t.Fatalf("expected a refusal naming the shape, got: %v", err)
		}
	})

	t.Run("a selector with neither tag nor tags is refused", func(t *testing.T) {
		_, err := parseTunnel(t, "connection:\n  tunnel:\n    target: { other: x }\n")
		if err == nil {
			t.Fatal("expected a selector with no tags to be refused")
		}
	})

	t.Run("Describe renders both forms for an error message", func(t *testing.T) {
		if got := (TunnelTarget{ID: "i-1"}).Describe(); got != "i-1" {
			t.Errorf("expected the id, got %q", got)
		}
		// Sorted, so two runs describe one selector the same way.
		got := TunnelTarget{Tags: map[string]string{"Role": "relay", "Env": "test"}}.Describe()
		if got != "Env=test,Role=relay" {
			t.Errorf("expected a sorted rendering, got %q", got)
		}
	})
}
