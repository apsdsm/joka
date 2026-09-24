package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

func file(path string, entities ...domain.Entity) *domain.EntityFile {
	return &domain.EntityFile{Path: path, Entities: entities}
}

func ent(table, refID string, children ...domain.Entity) domain.Entity {
	return domain.Entity{Table: table, RefID: refID, PKColumn: "id", Children: children}
}

func TestValidateEntitySet(t *testing.T) {
	t.Run("it accepts a set where every _id is present and unique", func(t *testing.T) {
		problems := ValidateEntitySet([]*domain.EntityFile{
			file("a.yaml", ent("fields", "alpha", ent("field_versions", "alpha_v1"))),
			file("b.yaml", ent("fields", "beta")),
		})

		if len(problems) != 0 {
			t.Errorf("expected no problems, got %+v", problems)
		}
		if err := EntitySetError(problems); err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})

	t.Run("it reports an entity with no _id", func(t *testing.T) {
		problems := ValidateEntitySet([]*domain.EntityFile{
			file("a.yaml", ent("fields", "alpha"), ent("fields", "")),
		})

		if len(problems) != 1 {
			t.Fatalf("expected 1 problem, got %+v", problems)
		}
		p := problems[0]
		if p.Kind != ProblemMissingID {
			t.Errorf("expected missing_id, got %q", p.Kind)
		}
		if p.Where[0].Position != 2 || p.Where[0].File != "a.yaml" {
			t.Errorf("expected a.yaml position 2, got %+v", p.Where[0])
		}
	})

	t.Run("it counts positions depth-first, matching insertion order", func(t *testing.T) {
		// A child without an _id must be reported at its flattened position,
		// not its position among its siblings.
		problems := ValidateEntitySet([]*domain.EntityFile{
			file("a.yaml",
				ent("fields", "alpha", ent("field_versions", "")),
				ent("fields", "beta"),
			),
		})

		if len(problems) != 1 {
			t.Fatalf("expected 1 problem, got %+v", problems)
		}
		if got := problems[0].Where[0].Position; got != 2 {
			t.Errorf("expected the child at depth-first position 2, got %d", got)
		}
	})

	t.Run("it reports an _id claimed by two files and names both", func(t *testing.T) {
		problems := ValidateEntitySet([]*domain.EntityFile{
			file("a.yaml", ent("fields", "alpha")),
			file("b.yaml", ent("fields", "alpha")),
		})

		if len(problems) != 1 {
			t.Fatalf("expected 1 problem, got %+v", problems)
		}
		p := problems[0]
		if p.Kind != ProblemDuplicateID || p.RefID != "alpha" {
			t.Errorf("expected a duplicate of alpha, got %+v", p)
		}
		if len(p.Where) != 2 {
			t.Fatalf("expected both claimants named, got %+v", p.Where)
		}
		if p.Where[0].File != "a.yaml" || p.Where[1].File != "b.yaml" {
			t.Errorf("expected both files in declaration order, got %+v", p.Where)
		}
	})

	t.Run("it reports an _id claimed twice within one file", func(t *testing.T) {
		problems := ValidateEntitySet([]*domain.EntityFile{
			file("a.yaml", ent("fields", "alpha"), ent("fields", "alpha")),
		})

		if len(problems) != 1 || problems[0].Kind != ProblemDuplicateID {
			t.Fatalf("expected one duplicate, got %+v", problems)
		}
		if len(problems[0].Where) != 2 {
			t.Errorf("expected both positions named, got %+v", problems[0].Where)
		}
	})

	t.Run("it reports every problem rather than the first", func(t *testing.T) {
		// A set that has never been validated usually has more than one, and
		// fixing them one error message at a time is miserable.
		problems := ValidateEntitySet([]*domain.EntityFile{
			file("a.yaml", ent("fields", ""), ent("fields", "dup")),
			file("b.yaml", ent("fields", ""), ent("fields", "dup")),
		})

		if len(problems) != 3 {
			t.Fatalf("expected 2 missing and 1 duplicate, got %+v", problems)
		}
	})

	t.Run("it ignores a nil file", func(t *testing.T) {
		if problems := ValidateEntitySet([]*domain.EntityFile{nil}); len(problems) != 0 {
			t.Errorf("expected no problems, got %+v", problems)
		}
	})

	t.Run("it scopes uniqueness to the set it is given", func(t *testing.T) {
		// Parallel per-environment sets (dev1/ and local/) deliberately share
		// _ids and are never loaded together, so each validates on its own.
		local := ValidateEntitySet([]*domain.EntityFile{file("local/a.yaml", ent("fields", "alpha"))})
		dev := ValidateEntitySet([]*domain.EntityFile{file("dev1/a.yaml", ent("fields", "alpha"))})

		if len(local) != 0 || len(dev) != 0 {
			t.Errorf("expected each set valid alone, got %+v and %+v", local, dev)
		}
	})
}

func TestEntitySetError(t *testing.T) {
	t.Run("it is nil for a valid set", func(t *testing.T) {
		if err := EntitySetError(nil); err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	t.Run("it names every problem and wraps the sentinel", func(t *testing.T) {
		err := EntitySetError(ValidateEntitySet([]*domain.EntityFile{
			file("a.yaml", ent("fields", ""), ent("users", "dup")),
			file("b.yaml", ent("users", "dup")),
		}))

		if !errors.Is(err, domain.ErrEntitySetInvalid) {
			t.Fatalf("expected ErrEntitySetInvalid, got %v", err)
		}
		for _, want := range []string{
			"1 entity without an _id",
			"1 _id claimed twice",
			"a.yaml entity #1 (fields)",
			`_id "dup" is claimed by`,
			"a.yaml entity #2 (users)",
			"b.yaml entity #1 (users)",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("expected %q in:\n%s", want, err)
			}
		}
	})

	t.Run("it reads correctly for a single missing _id", func(t *testing.T) {
		err := EntitySetError(ValidateEntitySet([]*domain.EntityFile{
			file("a.yaml", ent("fields", "")),
		}))
		if !strings.Contains(err.Error(), "1 entity without an _id") {
			t.Errorf("unexpected wording: %s", err)
		}
	})
}
