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

	return &domain.EntityFile{
		Path:     a.Path,
		Entities: entities,
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
		if k == "_is" || k == "_id" || k == "_has" || k == "_pk" {
			continue
		}

		columns[k] = v
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
	}, nil
}
