package app

import (
	"sort"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// ApplyOverrides merges every file's `overrides:` into the entities they name,
// and returns what was wrong with them.
//
// It runs once, at load, before anything reads the set. The result is one
// merged declaration, so the planner, the applier, the diff and the write-back
// all see the same thing — there is no second place that has to remember to
// apply an override, which is how the content hash and the tracking tables
// each drifted their own way before.
//
// The merge is per column, not per entity: an override naming one column
// leaves the rest of the declaration alone. Replacing the whole entity would
// mean repeating every column in every environment, which is the duplication
// this exists to remove.
func ApplyOverrides(files []*domain.EntityFile) []EntitySetProblem {
	declared := make(map[string]*domain.Entity)
	for _, file := range files {
		collectEntityPointers(file.Entities, declared)
	}

	var problems []EntitySetProblem
	claimed := make(map[string]string)

	for _, file := range files {
		for _, o := range file.Overrides {
			// Two overrides for one _id cannot both be the environment's
			// answer, and applying them in load order would make the result
			// depend on a filename.
			if first, taken := claimed[o.RefID]; taken {
				problems = append(problems, EntitySetProblem{
					Kind:  ProblemOverrideDuplicated,
					RefID: o.RefID,
					Where: []EntityLocation{{File: first}, {File: file.Path}},
				})
				continue
			}
			claimed[o.RefID] = file.Path

			// The same rule `moved:` has for to:. An override naming nothing
			// is a typo, and doing nothing quietly would leave the author
			// believing an environment was configured when it was not.
			target, found := declared[o.RefID]
			if !found {
				problems = append(problems, EntitySetProblem{
					Kind:  ProblemOverrideUndeclared,
					RefID: o.RefID,
					Where: []EntityLocation{{File: file.Path}},
				})
				continue
			}

			if target.Columns == nil {
				target.Columns = make(map[string]any, len(o.Columns))
			}
			for column, value := range o.Columns {
				target.Columns[column] = value
			}
		}
	}

	sort.Slice(problems, func(i, j int) bool { return problems[i].RefID < problems[j].RefID })

	return problems
}

// collectEntityPointers indexes every entity in a tree by _id, as a pointer so
// an override can be merged into the declaration the rest of the run reads.
func collectEntityPointers(entities []domain.Entity, out map[string]*domain.Entity) {
	for i := range entities {
		if entities[i].RefID != "" {
			out[entities[i].RefID] = &entities[i]
		}
		collectEntityPointers(entities[i].Children, out)
	}
}
