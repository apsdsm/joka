package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

func orderFile(path string, entities ...domain.Entity) *domain.EntityFile {
	return &domain.EntityFile{Path: path, Entities: entities}
}

func orderEntity(refID string, columns map[string]any) domain.Entity {
	return domain.Entity{Table: "t", RefID: refID, PKColumn: "id", Columns: columns}
}

func orderPaths(files []*domain.EntityFile) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}
	return out
}

func TestOrderFilesByReference(t *testing.T) {
	t.Run("a file is placed before the file that references it", func(t *testing.T) {
		// The reported failure: a staff seed referencing an org declared in a
		// file that sorts later died with "not found in reference map" on a
		// fresh database, and the workaround was to rewrite it as a lookup.
		in := []*domain.EntityFile{
			orderFile("a_staff.yaml", orderEntity("staff", map[string]any{"org_id": "{{ org.id }}"})),
			orderFile("b_orgs.yaml", orderEntity("org", map[string]any{"name": "Alpha"})),
		}

		out, err := OrderFilesByReference(in)
		if err != nil {
			t.Fatalf("OrderFilesByReference: %v", err)
		}
		if got := orderPaths(out); got[0] != "b_orgs.yaml" || got[1] != "a_staff.yaml" {
			t.Errorf("expected the org file first, got %v", got)
		}
	})

	t.Run("a set with no cross-file references is left alone", func(t *testing.T) {
		in := []*domain.EntityFile{
			orderFile("z.yaml", orderEntity("z1", map[string]any{"name": "z"})),
			orderFile("a.yaml", orderEntity("a1", map[string]any{"name": "a"})),
		}

		out, err := OrderFilesByReference(in)
		if err != nil {
			t.Fatalf("OrderFilesByReference: %v", err)
		}
		// Stability matters: reordering a set nothing constrains would move
		// insertion order, and with it every recorded position, for no reason.
		if got := orderPaths(out); got[0] != "z.yaml" || got[1] != "a.yaml" {
			t.Errorf("expected the original order, got %v", got)
		}
	})

	t.Run("a chain across three files is ordered end to end", func(t *testing.T) {
		in := []*domain.EntityFile{
			orderFile("3.yaml", orderEntity("c", map[string]any{"b_id": "{{ b.id }}"})),
			orderFile("2.yaml", orderEntity("b", map[string]any{"a_id": "{{ a.id }}"})),
			orderFile("1.yaml", orderEntity("a", map[string]any{"name": "a"})),
		}

		out, err := OrderFilesByReference(in)
		if err != nil {
			t.Fatalf("OrderFilesByReference: %v", err)
		}
		want := []string{"1.yaml", "2.yaml", "3.yaml"}
		for i, p := range orderPaths(out) {
			if p != want[i] {
				t.Fatalf("expected %v, got %v", want, orderPaths(out))
			}
		}
	})

	t.Run("a reference within one file constrains nothing", func(t *testing.T) {
		// Within a file, declaration order decides, and it is what the reader
		// sees. A self-edge would deadlock the sort.
		in := []*domain.EntityFile{
			orderFile("one.yaml",
				orderEntity("parent", map[string]any{"name": "p"}),
				orderEntity("child", map[string]any{"parent_id": "{{ parent.id }}"}),
			),
		}

		if _, err := OrderFilesByReference(in); err != nil {
			t.Fatalf("expected a single file to order cleanly, got: %v", err)
		}
	})

	t.Run("files that reference each other are refused, and named", func(t *testing.T) {
		in := []*domain.EntityFile{
			orderFile("a.yaml", orderEntity("a1", map[string]any{"b": "{{ b1.id }}"})),
			orderFile("b.yaml", orderEntity("b1", map[string]any{"a": "{{ a1.id }}"})),
		}

		_, err := OrderFilesByReference(in)
		if !errors.Is(err, domain.ErrInvalidReference) {
			t.Fatalf("expected ErrInvalidReference, got: %v", err)
		}
		for _, want := range []string{"a.yaml", "b.yaml"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("expected the refusal to name %q, got: %v", want, err)
			}
		}
	})

	t.Run("a reference the set does not declare is not an ordering problem", func(t *testing.T) {
		// It is either a row joka already tracks, or a mistake the resolver
		// reports with the name in it. Neither is this function's business.
		in := []*domain.EntityFile{
			orderFile("a.yaml", orderEntity("a1", map[string]any{"x": "{{ elsewhere.id }}"})),
		}

		if _, err := OrderFilesByReference(in); err != nil {
			t.Errorf("expected an unknown reference to be ignored here, got: %v", err)
		}
	})
}

func TestReferencesIn(t *testing.T) {
	t.Run("it finds a reference and ignores every other template", func(t *testing.T) {
		e := orderEntity("x", map[string]any{
			"org_id":  "{{ org.id }}",
			"made":    "{{ now }}",
			"pw":      "{{ argon2id|secret }}",
			"digest":  "{{ sha256|value }}",
			"looked":  "{{ lookup|t,id,code=X }}",
			"secret":  "{{ asm.seed.key }}",
			"literal": "plain",
			"number":  42,
		})

		refs := referencesIn(e)
		if len(refs) != 1 || refs[0] != "org" {
			t.Errorf("expected exactly the org reference, got %v", refs)
		}
	})
}
