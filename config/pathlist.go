package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// PathList is one path or several.
//
//	entities: ./entities
//	entities: [../shared/entities, ./entities]
//
// Both forms are accepted because the single one is what almost every project
// has and there is no reason to make them all change.
type PathList []string

// UnmarshalYAML accepts a scalar or a sequence.
func (p *PathList) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var one string
		if err := value.Decode(&one); err != nil {
			return err
		}
		if one == "" {
			*p = nil
			return nil
		}
		*p = PathList{one}

		return nil

	case yaml.SequenceNode:
		var many []string
		if err := value.Decode(&many); err != nil {
			return err
		}
		*p = PathList(many)

		return nil
	}

	return fmt.Errorf("line %d: expected a path or a list of paths", value.Line)
}

// IsZero lets an unset PathList stay out of a marshalled config.
func (p PathList) IsZero() bool { return len(p) == 0 }
