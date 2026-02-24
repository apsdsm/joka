package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

func TestResolveValue_PlainString(t *testing.T) {
	val, err := resolveValue("hello", nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if val != "hello" {
		t.Errorf("expected 'hello', got %v", val)
	}
}

func TestResolveValue_Now(t *testing.T) {
	val, err := resolveValue("{{ now }}", nil, "2025-01-01 00:00:00")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if val != "2025-01-01 00:00:00" {
		t.Errorf("expected timestamp, got %v", val)
	}
}

func TestResolveValue_NowTrimmed(t *testing.T) {
	// Test with no spaces around expression.
	val, err := resolveValue("{{now}}", nil, "2025-01-01 00:00:00")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if val != "2025-01-01 00:00:00" {
		t.Errorf("expected timestamp, got %v", val)
	}
}

func TestResolveValue_RefID(t *testing.T) {
	refMap := map[string]int64{"parent": 42}

	val, err := resolveValue("{{ parent.id }}", refMap, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	id, ok := val.(int64)
	if !ok {
		t.Fatalf("expected int64, got %T", val)
	}

	if id != 42 {
		t.Errorf("expected 42, got %d", id)
	}
}

func TestResolveValue_RefID_Missing(t *testing.T) {
	refMap := map[string]int64{}

	_, err := resolveValue("{{ missing.id }}", refMap, "")
	if err == nil {
		t.Fatal("expected error for missing ref, got nil")
	}

	if !errors.Is(err, domain.ErrInvalidReference) {
		t.Errorf("expected ErrInvalidReference, got: %v", err)
	}
}

func TestResolveValue_Argon2id(t *testing.T) {
	val, err := resolveValue("{{ argon2id|password123 }}", nil, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	hash, ok := val.(string)
	if !ok {
		t.Fatalf("expected string, got %T", val)
	}

	if !strings.HasPrefix(hash, "$argon2id$") {
		t.Errorf("expected argon2id hash prefix, got %q", hash)
	}
}

func TestResolveValue_UnknownExpression(t *testing.T) {
	_, err := resolveValue("{{ unknown_func }}", nil, "")
	if err == nil {
		t.Fatal("expected error for unknown expression, got nil")
	}

	if !errors.Is(err, domain.ErrInvalidTemplate) {
		t.Errorf("expected ErrInvalidTemplate, got: %v", err)
	}
}

func TestResolveColumns_MixedTypes(t *testing.T) {
	columns := map[string]any{
		"name":       "Alice",
		"created_at": "{{ now }}",
		"count":      42,
		"parent_id":  "{{ parent.id }}",
	}

	refMap := map[string]int64{"parent": 10}
	now := "2025-06-01 12:00:00"

	resolved, err := resolveColumns(columns, refMap, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resolved["name"] != "Alice" {
		t.Errorf("expected 'Alice', got %v", resolved["name"])
	}

	if resolved["created_at"] != now {
		t.Errorf("expected %q, got %v", now, resolved["created_at"])
	}

	if resolved["count"] != 42 {
		t.Errorf("expected 42, got %v", resolved["count"])
	}

	id, ok := resolved["parent_id"].(int64)
	if !ok {
		t.Fatalf("expected int64 for parent_id, got %T", resolved["parent_id"])
	}

	if id != 10 {
		t.Errorf("expected 10, got %d", id)
	}
}

func TestResolveColumns_ErrorPropagation(t *testing.T) {
	columns := map[string]any{
		"ref": "{{ missing.id }}",
	}

	_, err := resolveColumns(columns, map[string]int64{}, "")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
