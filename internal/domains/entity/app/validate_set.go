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
	// ProblemMoveTargetUndeclared is a `moved:` entry whose to: nothing
	// declares. The move would succeed and the very next rule would delete the
	// row, because a tracked entity no file declares is removed — so a typo in
	// to: is data loss. Requiring it to be declared is what catches that.
	ProblemMoveTargetUndeclared = "move_target_undeclared"
	// ProblemMoveSourceDeclared is a `moved:` entry whose from: something still
	// declares. The file says both "this entity exists" and "its record belongs
	// to another name", which cannot both be true.
	ProblemMoveSourceDeclared = "move_source_declared"
	// ProblemOverrideUndeclared is an `overrides:` entry whose _id nothing in
	// the set declares. There is nothing to override, so it is a typo — and
	// doing nothing quietly would leave the author believing an environment was
	// configured when it was not.
	ProblemOverrideUndeclared = "override_undeclared"
	// ProblemOverrideDuplicated is an _id overridden by more than one entry.
	// They cannot both be the environment's answer, and applying them in load
	// order would make the result depend on a filename.
	ProblemOverrideDuplicated = "override_duplicated"
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

	// A move's to: must be declared and its from: must not. Together those two
	// say the rename has actually happened in the file, which is the only
	// evidence joka has that the entry means what it says.
	for _, file := range files {
		if file == nil {
			continue
		}
		for _, m := range file.Moved {
			loc := EntityLocation{File: file.Path}

			if _, declared := claims[m.To]; !declared {
				problems = append(problems, EntitySetProblem{
					Kind: ProblemMoveTargetUndeclared, RefID: m.To, Where: []EntityLocation{loc},
				})
			}
			if _, declared := claims[m.From]; declared {
				problems = append(problems, EntitySetProblem{
					Kind: ProblemMoveSourceDeclared, RefID: m.From, Where: claims[m.From].locations,
				})
			}
		}
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
		case ProblemRemovedAndDeclared, ProblemMoveTargetUndeclared, ProblemMoveSourceDeclared,
			ProblemOverrideUndeclared, ProblemOverrideDuplicated:
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
		noun := "state operations that contradict"
		if conflicting == 1 {
			noun = "state operation that contradicts"
		}
		parts = append(parts, fmt.Sprintf("%d %s what the files declare", conflicting, noun))
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
		case ProblemMoveTargetUndeclared:
			fmt.Fprintf(&b, "\n  moved: to %q in %s, which no entity declares \u2014 the move would "+
				"succeed and the row would then be deleted for being declared nowhere",
				p.RefID, p.Where[0].File)
		case ProblemMoveSourceDeclared:
			fmt.Fprintf(&b, "\n  moved: from %q, which is still declared at:", p.RefID)
			for _, loc := range p.Where {
				fmt.Fprintf(&b, "\n    %s", loc)
			}
		case ProblemOverrideUndeclared:
			fmt.Fprintf(&b, "\n  overrides: %q in %s, which no entity declares \u2014 there is nothing to override",
				p.RefID, p.Where[0].File)
		case ProblemOverrideDuplicated:
			fmt.Fprintf(&b, "\n  _id %q is overridden more than once:", p.RefID)
			for _, loc := range p.Where {
				fmt.Fprintf(&b, "\n    %s", loc.File)
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
