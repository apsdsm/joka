package app

import (
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

func ovrFile(path string, entities []domain.Entity, overrides ...domain.Override) *domain.EntityFile {
	return &domain.EntityFile{Path: path, Entities: entities, Overrides: overrides}
}

func TestApplyOverrides(t *testing.T) {
	t.Run("it replaces the named columns and leaves the rest", func(t *testing.T) {
		// The point of the feature: an entity that differs in two fields per
		// environment was duplicated whole, so every later edit had to be made
		// in each copy or was forgotten in one.
		shared := ovrFile("shared/client.yaml", []domain.Entity{{
			Table: "clients", RefID: "c1", PKColumn: "id",
			Columns: map[string]any{"name": "LGC", "host": "localhost", "port": 3000},
		}})
		env := ovrFile("test/o.yaml", nil, domain.Override{
			RefID: "c1", Columns: map[string]any{"host": "test.example.com"},
		})

		if _, problems := ApplyOverrides([]*domain.EntityFile{shared, env}); len(problems) != 0 {
			t.Fatalf("expected no problems, got %+v", problems)
		}

		got := shared.Entities[0].Columns
		if got["host"] != "test.example.com" {
			t.Errorf("expected the override to win, got %v", got["host"])
		}
		if got["name"] != "LGC" || got["port"] != 3000 {
			t.Errorf("expected the untouched columns to survive, got %v", got)
		}
	})

	t.Run("it can add a column the entity does not declare", func(t *testing.T) {
		// "a field only this environment needs" is the same request as "a
		// field that differs". A mistyped name fails at the insert, naming it.
		shared := ovrFile("a.yaml", []domain.Entity{{
			Table: "t", RefID: "c1", Columns: map[string]any{"name": "x"},
		}})
		env := ovrFile("b.yaml", nil, domain.Override{
			RefID: "c1", Columns: map[string]any{"debug": true},
		})

		ApplyOverrides([]*domain.EntityFile{shared, env})

		if shared.Entities[0].Columns["debug"] != true {
			t.Error("expected the added column to be merged in")
		}
	})

	t.Run("it reaches a child entity", func(t *testing.T) {
		shared := ovrFile("a.yaml", []domain.Entity{{
			Table: "parents", RefID: "p", Columns: map[string]any{"n": 1},
			Children: []domain.Entity{{Table: "kids", RefID: "k", Columns: map[string]any{"n": 2}}},
		}})
		env := ovrFile("b.yaml", nil, domain.Override{RefID: "k", Columns: map[string]any{"n": 99}})

		if _, problems := ApplyOverrides([]*domain.EntityFile{shared, env}); len(problems) != 0 {
			t.Fatalf("expected no problems, got %+v", problems)
		}
		if got := shared.Entities[0].Children[0].Columns["n"]; got != 99 {
			t.Errorf("expected the child to be overridden, got %v", got)
		}
	})

	t.Run("an override naming nothing is a problem, not a silent no-op", func(t *testing.T) {
		env := ovrFile("b.yaml", nil, domain.Override{RefID: "nope", Columns: map[string]any{"x": 1}})

		_, problems := ApplyOverrides([]*domain.EntityFile{env})
		if len(problems) != 1 || problems[0].Kind != ProblemOverrideUndeclared {
			t.Fatalf("expected ProblemOverrideUndeclared, got %+v", problems)
		}
	})

	t.Run("two overrides for one _id are refused", func(t *testing.T) {
		// They cannot both be the environment's answer, and applying them in
		// load order would make the result depend on a filename.
		shared := ovrFile("a.yaml", []domain.Entity{{Table: "t", RefID: "c1", Columns: map[string]any{"n": 1}}})
		one := ovrFile("b.yaml", nil, domain.Override{RefID: "c1", Columns: map[string]any{"n": 2}})
		two := ovrFile("c.yaml", nil, domain.Override{RefID: "c1", Columns: map[string]any{"n": 3}})

		_, problems := ApplyOverrides([]*domain.EntityFile{shared, one, two})
		if len(problems) != 1 || problems[0].Kind != ProblemOverrideDuplicated {
			t.Fatalf("expected ProblemOverrideDuplicated, got %+v", problems)
		}
		if shared.Entities[0].Columns["n"] != 2 {
			t.Errorf("expected the first override to have been applied before the clash, got %v",
				shared.Entities[0].Columns["n"])
		}
	})

	t.Run("no overrides changes nothing", func(t *testing.T) {
		shared := ovrFile("a.yaml", []domain.Entity{{Table: "t", RefID: "c1", Columns: map[string]any{"n": 1}}})

		if _, problems := ApplyOverrides([]*domain.EntityFile{shared}); len(problems) != 0 {
			t.Fatalf("expected no problems, got %+v", problems)
		}
		if shared.Entities[0].Columns["n"] != 1 {
			t.Error("expected the declaration to be untouched")
		}
	})
}
