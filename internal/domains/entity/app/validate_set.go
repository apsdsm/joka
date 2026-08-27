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

	missing, duplicate := 0, 0
	for _, p := range problems {
		if p.Kind == ProblemMissingID {
			missing++
		} else {
			duplicate++
		}
	}

	switch {
	case missing > 0 && duplicate > 0:
		fmt.Fprintf(&b, "%s without an _id and %s claimed twice", countOf(missing, "entity", "entities"), countOf(duplicate, "_id", "_ids"))
	case missing > 0:
		fmt.Fprintf(&b, "%s without an _id", countOf(missing, "entity", "entities"))
	default:
		fmt.Fprintf(&b, "%s claimed twice", countOf(duplicate, "_id", "_ids"))
	}

	for _, p := range problems {
		switch p.Kind {
		case ProblemMissingID:
			fmt.Fprintf(&b, "\n  no _id: %s", p.Where[0])
		case ProblemDuplicateID:
			fmt.Fprintf(&b, "\n  _id %q is claimed by:", p.RefID)
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
