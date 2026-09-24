package app

import (
	"fmt"
	"os"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
	"gopkg.in/yaml.v3"
)

// ParseEntityAction reads a YAML entity file and converts it into an
// EntityFile. The YAML structure uses reserved keys (_is for table name,
// _id for reference handle, _has for children) and treats all other keys
// as column→value pairs.
type ParseEntityAction struct {
	Path string
}

// yamlFile is the top-level YAML structure for an entity file.
type yamlFile struct {
	Entities []map[string]any `yaml:"entities"`
	Removed  []yamlRemoval    `yaml:"removed"`
	Moved    []yamlMove       `yaml:"moved"`
}

// yamlMove is one entry of a file's `moved:` list.
type yamlMove struct {
	From string `yaml:"from"`
	To   string `yaml:"to"`
}

// yamlRemoval is one entry of a file's `removed:` list.
type yamlRemoval struct {
	ID   string `yaml:"_id"`
	Keep bool   `yaml:"keep"`
}

// Execute reads the YAML file at Path, parses each entity in the entities
// list, and returns an EntityFile.
func (a ParseEntityAction) Execute() (*domain.EntityFile, error) {
	data, err := os.ReadFile(a.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: reading %s: %v", domain.ErrEntityParseFailed, a.Path, err)
	}

	var file yamlFile

	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("%w: parsing %s: %v", domain.ErrEntityParseFailed, a.Path, err)
	}

	entities, err := parseEntities(file.Entities)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", domain.ErrEntityParseFailed, err)
	}

	removed := make([]domain.Removal, 0, len(file.Removed))
	for _, r := range file.Removed {
		if r.ID == "" {
			return nil, fmt.Errorf("%w: a removed: entry in %s has no _id",
				domain.ErrEntityParseFailed, a.Path)
		}
		removed = append(removed, domain.Removal{RefID: r.ID, Keep: r.Keep})
	}

	moved := make([]domain.Move, 0, len(file.Moved))
	for _, m := range file.Moved {
		if m.From == "" || m.To == "" {
			return nil, fmt.Errorf("%w: a moved: entry in %s needs both from: and to:",
				domain.ErrEntityParseFailed, a.Path)
		}
		moved = append(moved, domain.Move{From: m.From, To: m.To})
	}

	return &domain.EntityFile{
		Path:     a.Path,
		Entities: entities,
		Removed:  removed,
		Moved:    moved,
	}, nil
}

// parseEntities converts a slice of raw YAML maps into domain entities by
// separating reserved keys (_is, _id, _has) from column data.
func parseEntities(raw []map[string]any) ([]domain.Entity, error) {
	var entities []domain.Entity

	for _, entry := range raw {
		entity, err := parseEntity(entry)
		if err != nil {
			return nil, err
		}

		entities = append(entities, entity)
	}

	return entities, nil
}

// parseEntity converts a single raw YAML map into a domain Entity.
func parseEntity(raw map[string]any) (domain.Entity, error) {
	table, ok := raw["_is"].(string)
	if !ok || table == "" {
		return domain.Entity{}, fmt.Errorf("entity missing required _is key")
	}

	refID, _ := raw["_id"].(string)

	pkColumn := "id"
	if pk, ok := raw["_pk"].(string); ok && pk != "" {
		pkColumn = pk
	}

	columns := make(map[string]any, len(raw))

	for k, v := range raw {
		if k == "_is" || k == "_id" || k == "_has" || k == "_pk" || k == "_once" {
			continue
		}

		columns[k] = v
	}

	once, err := parseOnce(raw, table, columns)
	if err != nil {
		return domain.Entity{}, err
	}

	var children []domain.Entity

	if hasRaw, ok := raw["_has"]; ok {
		childList, ok := hasRaw.([]any)
		if !ok {
			return domain.Entity{}, fmt.Errorf("_has must be a list")
		}

		for _, childRaw := range childList {
			childMap, ok := childRaw.(map[string]any)
			if !ok {
				return domain.Entity{}, fmt.Errorf("_has entries must be maps")
			}

			child, err := parseEntity(childMap)
			if err != nil {
				return domain.Entity{}, err
			}

			children = append(children, child)
		}
	}

	return domain.Entity{
		Table:    table,
		RefID:    refID,
		PKColumn: pkColumn,
		Columns:  columns,
		Children: children,
		Once:     once,
	}, nil
}

// parseOnce reads the _once list: the columns joka seeds and then leaves to the
// database.
//
// The columns themselves stay where every other column is, so a reader sees the
// whole row in one place and a template in a _once column resolves the same way
// as anywhere else. _once only annotates them.
//
// A name that the entity does not declare is refused. There is no way to seed a
// column that is not there, so it is a typo, and silently ignoring it would
// leave the author believing a column was protected when it was not.
func parseOnce(raw map[string]any, table string, columns map[string]any) ([]string, error) {
	value, ok := raw["_once"]
	if !ok {
		return nil, nil
	}

	list, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("_once must be a list of column names (%s)", table)
	}

	out := make([]string, 0, len(list))

	for _, item := range list {
		name, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("_once entries must be column names (%s)", table)
		}
		if _, declared := columns[name]; !declared {
			return nil, fmt.Errorf("_once names %q, which %s does not declare", name, table)
		}
		out = append(out, name)
	}

	return out, nil
}
