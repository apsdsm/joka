package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// EntitySetProblem is one thing wrong with an entity set: an entity with no
// _id, or an _id claimed by more than one entity.
type EntitySetProblem struct {
	// Kind is why it is a problem.
	Kind string `json:"kind"`
	// RefID is the offending _id, empty for a missing one.
	RefID string `json:"ref_id,omitempty"`
	// Where names the file and position of each entity involved. A duplicate
	// names both (or all) claimants.
	Where []EntityLocation `json:"where"`
}

// Problem kinds.
const (
	// ProblemMissingID is an entity with no _id. joka identifies every seeded
	// row by _id, so an entity without one cannot be tracked across an edit.
	ProblemMissingID = "missing_id"
	// ProblemDuplicateID is an _id claimed by more than one entity in the set.
	ProblemDuplicateID = "duplicate_id"
	// ProblemRemovedAndDeclared is an _id that a `removed:` entry names and an
	// entity still declares. The file says both keep this and stop tracking it,
	// and joka will not pick one.
	ProblemRemovedAndDeclared = "removed_and_declared"
)

// EntityLocation points at one entity in the set.
type EntityLocation struct {
	File string `json:"file"`
	// Position is the entity's 1-based depth-first position within its file.
	Position int    `json:"position"`
	Table    string `json:"table"`
}

func (l EntityLocation) String() string {
	return fmt.Sprintf("%s entity #%d (%s)", l.File, l.Position, l.Table)
}

// ValidateEntitySet checks the invariants an _id-keyed identity model needs:
// every entity declares an _id, and no _id is claimed twice.
//
// The scope is the set joka loaded for this run — whatever the resolved
// `entities:` directory contains — not the whole filesystem and not the
// database. Parallel per-environment sets (a dev1/ and a local/ tree of the
// same seeds) deliberately share _ids and are never loaded together, so
// checking any wider scope would reject a correct arrangement.
//
// It returns every problem it finds rather than the first, because a set that
// has never been validated usually has more than one and fixing them one error
// message at a time is miserable.
func ValidateEntitySet(files []*domain.EntityFile) []EntitySetProblem {
	type claim struct {
		locations []EntityLocation
	}

	claims := make(map[string]*claim)
	var missing []EntityLocation

	for _, file := range files {
		if file == nil {
			continue
		}

		pos := 0
		walkEntities(file.Entities, func(e domain.Entity) {
			pos++
			loc := EntityLocation{File: file.Path, Position: pos, Table: e.Table}

			if e.RefID == "" {
				missing = append(missing, loc)
				return
			}

			c, ok := claims[e.RefID]
			if !ok {
				c = &claim{}
				claims[e.RefID] = c
			}
			c.locations = append(c.locations, loc)
		})
	}

	var problems []EntitySetProblem

	// An _id cannot be both declared and removed. The two say opposite things
	// about the same row, and guessing which the author meant would be worse
	// than refusing.
	var removed []string
	for _, file := range files {
		if file == nil {
			continue
		}
		for _, r := range file.Removed {
			if _, declared := claims[r.RefID]; declared {
				removed = append(removed, r.RefID)
			}
		}
	}
	sort.Strings(removed)

	for _, refID := range removed {
		problems = append(problems, EntitySetProblem{
			Kind:  ProblemRemovedAndDeclared,
			RefID: refID,
			Where: claims[refID].locations,
		})
	}

	for _, loc := range missing {
		problems = append(problems, EntitySetProblem{Kind: ProblemMissingID, Where: []EntityLocation{loc}})
	}

	duplicates := make([]string, 0, len(claims))
	for refID, c := range claims {
		if len(c.locations) > 1 {
			duplicates = append(duplicates, refID)
		}
	}
	sort.Strings(duplicates)

	for _, refID := range duplicates {
		problems = append(problems, EntitySetProblem{
			Kind:  ProblemDuplicateID,
			RefID: refID,
			Where: claims[refID].locations,
		})
	}

	return problems
}

// EntitySetError turns the problems into one error naming all of them, or nil
// when there are none.
func EntitySetError(problems []EntitySetProblem) error {
	if len(problems) == 0 {
		return nil
	}

	var b strings.Builder
	sentinel := domain.ErrEntitySetInvalid

	missing, duplicate, conflicting := 0, 0, 0
	for _, p := range problems {
		switch p.Kind {
		case ProblemMissingID:
			missing++
		case ProblemRemovedAndDeclared:
			conflicting++
		default:
			duplicate++
		}
	}

	var parts []string
	if missing > 0 {
		parts = append(parts, countOf(missing, "entity", "entities")+" without an _id")
	}
	if duplicate > 0 {
		parts = append(parts, countOf(duplicate, "_id", "_ids")+" claimed twice")
	}
	if conflicting > 0 {
		parts = append(parts, countOf(conflicting, "_id", "_ids")+" both declared and removed")
	}
	b.WriteString(strings.Join(parts, ", "))

	for _, p := range problems {
		switch p.Kind {
		case ProblemMissingID:
			fmt.Fprintf(&b, "\n  no _id: %s", p.Where[0])
		case ProblemDuplicateID:
			fmt.Fprintf(&b, "\n  _id %q is claimed by:", p.RefID)
			for _, loc := range p.Where {
				fmt.Fprintf(&b, "\n    %s", loc)
			}
		case ProblemRemovedAndDeclared:
			fmt.Fprintf(&b, "\n  _id %q is named by a removed: entry and still declared at:", p.RefID)
			for _, loc := range p.Where {
				fmt.Fprintf(&b, "\n    %s", loc)
			}
		}
	}

	return fmt.Errorf("%w: %s", sentinel, b.String())
}

// walkEntities visits a graph depth-first pre-order — the same order entities
// are inserted and tracked in, so a reported position matches what every other
// command counts.
func walkEntities(entities []domain.Entity, fn func(domain.Entity)) {
	for _, e := range entities {
		fn(e)
		walkEntities(e.Children, fn)
	}
}

func countOf(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}
	return fmt.Sprintf("%d %s", n, plural)
}
