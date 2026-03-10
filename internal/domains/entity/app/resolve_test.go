package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// lookupMock implements DBAdapter.LookupValue for resolve tests. Only
// LookupValue is used; other methods panic if called unexpectedly.
type lookupMock struct {
	data map[string]any
}

func (l *lookupMock) EnsureTrackingTable(context.Context) error                                { panic("unused") }
func (l *lookupMock) EnsureRowTrackingTable(context.Context) error                             { panic("unused") }
func (l *lookupMock) EnsureContentHashColumn(context.Context) error                            { panic("unused") }
func (l *lookupMock) IsEntitySynced(context.Context, string) (bool, error)                     { panic("unused") }
func (l *lookupMock) RecordEntitySynced(context.Context, string) error                         { panic("unused") }
func (l *lookupMock) RecordEntitySyncedWithHash(context.Context, string, string) error         { panic("unused") }
func (l *lookupMock) UpdateEntitySynced(context.Context, string, string) error                 { panic("unused") }
func (l *lookupMock) GetEntityHash(context.Context, string) (string, error)                    { panic("unused") }
func (l *lookupMock) GetAllSyncedEntities(context.Context) (map[string]string, error)          { panic("unused") }
func (l *lookupMock) RecordEntityRow(context.Context, domain.TrackedRow) error                 { panic("unused") }
func (l *lookupMock) GetTrackedRows(context.Context, string) ([]domain.TrackedRow, error)      { panic("unused") }
func (l *lookupMock) DeleteTrackedRows(context.Context, string) error                          { panic("unused") }
func (l *lookupMock) DeleteRow(context.Context, string, string, int64) error                   { panic("unused") }
func (l *lookupMock) DeleteEntityRecord(context.Context, string) error                         { panic("unused") }
func (l *lookupMock) InsertRow(context.Context, string, map[string]any, string) (int64, error) { panic("unused") }
func (l *lookupMock) LookupValue(_ context.Context, table, returnCol, whereCol string, whereVal any) (any, error) {
	key := fmt.Sprintf("%s.%s.%s=%v", table, returnCol, whereCol, whereVal)

	val, ok := l.data[key]
	if !ok {
		return nil, fmt.Errorf("%w: %s.%s where %s=%v", domain.ErrLookupNotFound, table, returnCol, whereCol, whereVal)
	}

	return val, nil
}

func TestResolveValue(t *testing.T) {
	t.Run("it returns plain strings unchanged", func(t *testing.T) {
		val, err := resolveValue(context.Background(), "hello", nil, "", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if val != "hello" {
			t.Errorf("expected 'hello', got %v", val)
		}
	})

	t.Run("it resolves {{ now }} to the provided timestamp", func(t *testing.T) {
		val, err := resolveValue(context.Background(), "{{ now }}", nil, "2025-01-01 00:00:00", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if val != "2025-01-01 00:00:00" {
			t.Errorf("expected timestamp, got %v", val)
		}
	})

	t.Run("it resolves {{now}} without spaces", func(t *testing.T) {
		val, err := resolveValue(context.Background(), "{{now}}", nil, "2025-01-01 00:00:00", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if val != "2025-01-01 00:00:00" {
			t.Errorf("expected timestamp, got %v", val)
		}
	})

	t.Run("it resolves ref ID expressions to the stored PK", func(t *testing.T) {
		refMap := map[string]int64{"parent": 42}

		val, err := resolveValue(context.Background(), "{{ parent.id }}", refMap, "", nil)
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
	})

	t.Run("it returns ErrInvalidReference for missing ref IDs", func(t *testing.T) {
		refMap := map[string]int64{}

		_, err := resolveValue(context.Background(), "{{ missing.id }}", refMap, "", nil)
		if err == nil {
			t.Fatal("expected error for missing ref, got nil")
		}

		if !errors.Is(err, domain.ErrInvalidReference) {
			t.Errorf("expected ErrInvalidReference, got: %v", err)
		}
	})

	t.Run("it resolves argon2id expressions to a hash", func(t *testing.T) {
		val, err := resolveValue(context.Background(), "{{ argon2id|password123 }}", nil, "", nil)
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
	})

	t.Run("it resolves sha256 expressions to a hex digest", func(t *testing.T) {
		val, err := resolveValue(context.Background(), "{{ sha256|password }}", nil, "", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		hash, ok := val.(string)
		if !ok {
			t.Fatalf("expected string, got %T", val)
		}

		// SHA-256 of "password"
		expected := "5e884898da28047151d0e56f8dc6292773603d0d6aabbdd62a11ef721d1542d8"
		if hash != expected {
			t.Errorf("expected %q, got %q", expected, hash)
		}
	})

	t.Run("it returns ErrInvalidTemplate for unknown expressions", func(t *testing.T) {
		_, err := resolveValue(context.Background(), "{{ unknown_func }}", nil, "", nil)
		if err == nil {
			t.Fatal("expected error for unknown expression, got nil")
		}

		if !errors.Is(err, domain.ErrInvalidTemplate) {
			t.Errorf("expected ErrInvalidTemplate, got: %v", err)
		}
	})

	t.Run("it resolves lookup expressions to the queried value", func(t *testing.T) {
		db := &lookupMock{data: map[string]any{
			"industry_types.id.code=RESTAURANT": int64(7),
		}}

		val, err := resolveValue(context.Background(), "{{ lookup|industry_types,id,code=RESTAURANT }}", nil, "", db)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		id, ok := val.(int64)
		if !ok {
			t.Fatalf("expected int64, got %T", val)
		}

		if id != 7 {
			t.Errorf("expected 7, got %d", id)
		}
	})

	t.Run("it returns ErrLookupNotFound when lookup has no match", func(t *testing.T) {
		db := &lookupMock{data: map[string]any{}}

		_, err := resolveValue(context.Background(), "{{ lookup|industry_types,id,code=MISSING }}", nil, "", db)
		if err == nil {
			t.Fatal("expected error for missing lookup, got nil")
		}

		if !errors.Is(err, domain.ErrLookupNotFound) {
			t.Errorf("expected ErrLookupNotFound, got: %v", err)
		}
	})

	t.Run("it returns ErrInvalidTemplate for lookup with bad params", func(t *testing.T) {
		_, err := resolveValue(context.Background(), "{{ lookup|just_table }}", nil, "", nil)
		if err == nil {
			t.Fatal("expected error for bad lookup params, got nil")
		}

		if !errors.Is(err, domain.ErrInvalidTemplate) {
			t.Errorf("expected ErrInvalidTemplate, got: %v", err)
		}
	})

	t.Run("it returns ErrInvalidTemplate for lookup where clause missing equals", func(t *testing.T) {
		_, err := resolveValue(context.Background(), "{{ lookup|table,col,no_equals }}", nil, "", nil)
		if err == nil {
			t.Fatal("expected error for missing =, got nil")
		}

		if !errors.Is(err, domain.ErrInvalidTemplate) {
			t.Errorf("expected ErrInvalidTemplate, got: %v", err)
		}
	})
}

func TestResolveColumns(t *testing.T) {
	t.Run("it resolves mixed column types including templates and refs", func(t *testing.T) {
		columns := map[string]any{
			"name":       "Alice",
			"created_at": "{{ now }}",
			"count":      42,
			"parent_id":  "{{ parent.id }}",
		}

		refMap := map[string]int64{"parent": 10}
		now := "2025-06-01 12:00:00"

		resolved, err := resolveColumns(context.Background(), columns, refMap, now, nil)
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
	})

	t.Run("it propagates resolution errors", func(t *testing.T) {
		columns := map[string]any{
			"ref": "{{ missing.id }}",
		}

		_, err := resolveColumns(context.Background(), columns, map[string]int64{}, "", nil)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}
