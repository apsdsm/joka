package app

import (
	"fmt"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// ValidateRefIDs checks that no two entities in the tree share the same _id.
// Returns ErrDuplicateRefID wrapping the duplicate handle if found.
func ValidateRefIDs(entities []domain.Entity) error {
	seen := make(map[string]bool)
	return walkRefIDs(entities, seen)
}

func walkRefIDs(entities []domain.Entity, seen map[string]bool) error {
	for _, e := range entities {
		if e.RefID != "" {
			if seen[e.RefID] {
				return fmt.Errorf("%w: %q", domain.ErrDuplicateRefID, e.RefID)
			}
			seen[e.RefID] = true
		}
		if err := walkRefIDs(e.Children, seen); err != nil {
			return err
		}
	}
	return nil
}
