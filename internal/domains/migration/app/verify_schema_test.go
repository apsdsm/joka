package app

import (
	"context"
	"reflect"
	"testing"
)

func TestVerifySchema(t *testing.T) {
	ctx := context.Background()

	t.Run("it reports no drift when live matches the snapshot", func(t *testing.T) {
		adapter := &mockDBAdapter{
			latestSnapshotIndex: "240101000000",
			schemaSnapshot:      `{"users":"CREATE TABLE users (id INT)"}`,
			computedSchema:      map[string]string{"users": "CREATE TABLE users (id INT)"},
		}

		result, err := VerifySchemaAction{DB: adapter}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.HasDrift() {
			t.Errorf("expected no drift, got %+v", result)
		}
	})

	t.Run("it reports added tables present in live but not in snapshot", func(t *testing.T) {
		adapter := &mockDBAdapter{
			latestSnapshotIndex: "240101000000",
			schemaSnapshot:      `{"users":"CREATE TABLE users (id INT)"}`,
			computedSchema: map[string]string{
				"users":      "CREATE TABLE users (id INT)",
				"debug_logs": "CREATE TABLE debug_logs (id INT)",
			},
		}

		result, err := VerifySchemaAction{DB: adapter}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !reflect.DeepEqual(result.Added, []string{"debug_logs"}) {
			t.Errorf("expected Added=[debug_logs], got %v", result.Added)
		}
		if len(result.Removed) != 0 || len(result.Modified) != 0 {
			t.Errorf("unexpected diff: %+v", result)
		}
	})

	t.Run("it reports removed tables present in snapshot but missing from live", func(t *testing.T) {
		adapter := &mockDBAdapter{
			latestSnapshotIndex: "240101000000",
			schemaSnapshot: `{
				"users":"CREATE TABLE users (id INT)",
				"old_table":"CREATE TABLE old_table (id INT)"
			}`,
			computedSchema: map[string]string{"users": "CREATE TABLE users (id INT)"},
		}

		result, err := VerifySchemaAction{DB: adapter}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !reflect.DeepEqual(result.Removed, []string{"old_table"}) {
			t.Errorf("expected Removed=[old_table], got %v", result.Removed)
		}
	})

	t.Run("it reports modified tables when CREATE statements differ", func(t *testing.T) {
		adapter := &mockDBAdapter{
			latestSnapshotIndex: "240101000000",
			schemaSnapshot:      `{"users":"CREATE TABLE users (id INT)"}`,
			computedSchema:      map[string]string{"users": "CREATE TABLE users (id INT, email VARCHAR(255))"},
		}

		result, err := VerifySchemaAction{DB: adapter}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(result.Modified) != 1 || result.Modified[0].Table != "users" {
			t.Errorf("expected Modified=[users], got %+v", result.Modified)
		}
	})

	t.Run("it treats MySQL AUTO_INCREMENT counter changes as equivalent", func(t *testing.T) {
		adapter := &mockDBAdapter{
			latestSnapshotIndex: "240101000000",
			schemaSnapshot:      `{"users":"CREATE TABLE users (id INT AUTO_INCREMENT) ENGINE=InnoDB AUTO_INCREMENT=1 DEFAULT CHARSET=utf8mb4"}`,
			computedSchema: map[string]string{
				"users": "CREATE TABLE users (id INT AUTO_INCREMENT) ENGINE=InnoDB AUTO_INCREMENT=999 DEFAULT CHARSET=utf8mb4",
			},
		}

		result, err := VerifySchemaAction{DB: adapter}.Execute(ctx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if result.HasDrift() {
			t.Errorf("expected no drift after AUTO_INCREMENT normalization, got %+v", result)
		}
	})
}
