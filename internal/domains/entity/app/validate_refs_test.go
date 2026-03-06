package app

import (
	"errors"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

func TestValidateRefIDs_NoDuplicates(t *testing.T) {
	entities := []domain.Entity{
		{Table: "users", RefID: "user1", Columns: map[string]any{"name": "A"}},
		{Table: "users", RefID: "user2", Columns: map[string]any{"name": "B"}},
	}

	if err := ValidateRefIDs(entities); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRefIDs_DuplicateInSameLevel(t *testing.T) {
	entities := []domain.Entity{
		{Table: "users", RefID: "dup", Columns: map[string]any{"name": "A"}},
		{Table: "users", RefID: "dup", Columns: map[string]any{"name": "B"}},
	}

	err := ValidateRefIDs(entities)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !errors.Is(err, domain.ErrDuplicateRefID) {
		t.Errorf("expected ErrDuplicateRefID, got: %v", err)
	}
}

func TestValidateRefIDs_DuplicateInChildren(t *testing.T) {
	entities := []domain.Entity{
		{
			Table:   "users",
			RefID:   "shared",
			Columns: map[string]any{"name": "Parent"},
			Children: []domain.Entity{
				{Table: "profiles", RefID: "shared", Columns: map[string]any{"bio": "test"}},
			},
		},
	}

	err := ValidateRefIDs(entities)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !errors.Is(err, domain.ErrDuplicateRefID) {
		t.Errorf("expected ErrDuplicateRefID, got: %v", err)
	}
}

func TestValidateRefIDs_NoRefIDs(t *testing.T) {
	entities := []domain.Entity{
		{Table: "users", Columns: map[string]any{"name": "A"}},
		{Table: "users", Columns: map[string]any{"name": "B"}},
	}

	if err := ValidateRefIDs(entities); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateRefIDs_EmptySlice(t *testing.T) {
	if err := ValidateRefIDs([]domain.Entity{}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
