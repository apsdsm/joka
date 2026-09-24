package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// referencesIn returns the _id handles an entity's declared columns resolve
// against — every `{{ <ref>.id }}`.
//
// It matches resolveValue's parsing rather than approximating it with a regular
// expression, so a value the resolver will treat as a reference is a value this
// treats as one. Anything with its own prefix (lookup|, sha256|, argon2id|, a
// secret) is not a reference and is skipped, and so is `{{ now }}`.
func referencesIn(e domain.Entity) []string {
	var refs []string

	for _, value := range e.Columns {
		str, ok := value.(string)
		if !ok {
			continue
		}

		trimmed := strings.TrimSpace(str)
		if !strings.HasPrefix(trimmed, "{{") || !strings.HasSuffix(trimmed, "}}") {
			continue
		}

		expr := strings.TrimSpace(trimmed[2 : len(trimmed)-2])
		if expr == "now" || strings.Contains(expr, "|") || isSecretRef(expr) {
			continue
		}

		if strings.HasSuffix(expr, ".id") {
			refs = append(refs, strings.TrimSuffix(expr, ".id"))
		}
	}

	sort.Strings(refs)

	return refs
}

// OrderFilesByReference returns files ordered so that a file declaring an _id
// comes before any file whose entities reference it.
//
// A `{{ ref.id }}` resolves against a map that holds every tracked row from the
// start plus whatever this run has inserted so far, so on a database that
// already has the target the order never mattered. On a fresh one it decided
// everything, and the order was the filename: a seed referencing an entity
// declared in a file sorting later failed with `not found in reference map`,
// and the workaround was to rewrite the reference as a `{{ lookup| }}` — which
// gives up the identity joka is built on to work around an alphabetical
// accident.
//
// Ordering is by reference, stable on the original order for anything the
// references do not constrain, so a set with no cross-file references is
// untouched.
//
// Order *within* a file is left alone. Depth-first declaration order is what a
// reader sees and what `_has` nesting means, and the recorded position is the
// insertion position that deletes are sequenced by. An entity referencing a
// later entity in the same file is still an error, and the fix is to move it
// up — which is visible in the file, where a cross-file ordering problem was
// not.
func OrderFilesByReference(files []*domain.EntityFile) ([]*domain.EntityFile, error) {
	declaredIn := make(map[string]int)
	for i, file := range files {
		for _, e := range flattenEntities(file.Entities, nil) {
			if e.RefID != "" {
				declaredIn[e.RefID] = i
			}
		}
	}

	// needs[i] is the set of files that must come before file i.
	needs := make([]map[int]bool, len(files))
	for i := range files {
		needs[i] = map[int]bool{}

		for _, e := range flattenEntities(files[i].Entities, nil) {
			for _, ref := range referencesIn(e) {
				// A reference to something this set does not declare is not an
				// ordering problem. It is either tracked already, or it is a
				// mistake the resolver reports with the name in it.
				target, declared := declaredIn[ref]
				if declared && target != i {
					needs[i][target] = true
				}
			}
		}
	}

	ordered := make([]*domain.EntityFile, 0, len(files))
	done := make([]bool, len(files))

	for len(ordered) < len(files) {
		// Lowest original index whose dependencies are all placed, which is
		// what keeps the result stable: nothing moves that did not have to.
		next := -1
		for i := range files {
			if done[i] {
				continue
			}

			ready := true
			for need := range needs[i] {
				if !done[need] {
					ready = false
					break
				}
			}

			if ready {
				next = i
				break
			}
		}

		if next == -1 {
			return nil, referenceCycle(files, done)
		}

		done[next] = true
		ordered = append(ordered, files[next])
	}

	return ordered, nil
}

// referenceCycle names the files that reference each other, which is the only
// way OrderFilesByReference can fail. Two entities whose columns each need the
// other's primary key cannot both be inserted first, so this is unsatisfiable
// rather than merely unordered, and saying which files are involved is the
// whole value of noticing.
func referenceCycle(files []*domain.EntityFile, done []bool) error {
	var stuck []string
	for i := range files {
		if !done[i] {
			stuck = append(stuck, files[i].Path)
		}
	}
	sort.Strings(stuck)

	return fmt.Errorf("%w: these files reference each other, so none can be inserted first:\n  %s",
		domain.ErrInvalidReference, strings.Join(stuck, "\n  "))
}
