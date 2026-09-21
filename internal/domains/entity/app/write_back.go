package app

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrNotWritable means a column's declared value cannot be replaced with the
// one the database holds.
//
// The value is an expression — `{{ lookup|… }}`, `{{ ref.id }}`, `{{ now }}`, a
// secret — and writing a literal over it would replace the indirection with
// whatever it happened to resolve to this time. The reference would be gone and
// nothing would say so.
var ErrNotWritable = fmt.Errorf("the declared value is a template, so the database's value cannot replace it")

// SetEntityColumn rewrites one column of one entity in a YAML file, in place.
//
// It edits the parsed node tree rather than re-serialising a decoded value, so
// comments, key order, quoting style and the `_has:` nesting all survive. That
// matters because these are files a person maintains: a write-back that
// reformatted the file would make the diff unreadable and the feature
// unusable.
//
// The one thing it does not preserve is indentation width — yaml.v3 re-encodes
// at a fixed indent. A file written with four spaces comes back with two.
//
// refID is the entity's `_id`; the search descends through `_has:`.
func SetEntityColumn(path, refID, column, value string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	var root yaml.Node
	if err := yaml.Unmarshal(body, &root); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	entity := findEntity(&root, refID)
	if entity == nil {
		return fmt.Errorf("%s declares no entity with _id %q", path, refID)
	}

	target := mappingValue(entity, column)
	if target == nil {
		return fmt.Errorf("_id %q in %s declares no column %q", refID, path, column)
	}

	if isTemplateString(target.Value) {
		return fmt.Errorf("%w: _id %q column %q is %s", ErrNotWritable, refID, column, target.Value)
	}

	retypeScalar(target, value)

	return writeYAML(path, &root)
}

// isTemplateString reports whether a declared value is an expression rather
// than a literal.
func isTemplateString(s string) bool {
	return strings.Contains(s, "{{") && strings.Contains(s, "}}")
}

// findEntity walks the document for the entity carrying refID, descending
// through `_has:` the way every other walk in the domain does.
func findEntity(root *yaml.Node, refID string) *yaml.Node {
	if root.Kind == yaml.DocumentNode {
		if len(root.Content) == 0 {
			return nil
		}
		root = root.Content[0]
	}

	entities := mappingValue(root, "entities")
	if entities == nil {
		return nil
	}

	return findInSequence(entities, refID)
}

func findInSequence(seq *yaml.Node, refID string) *yaml.Node {
	if seq.Kind != yaml.SequenceNode {
		return nil
	}

	for _, item := range seq.Content {
		if item.Kind != yaml.MappingNode {
			continue
		}

		if id := mappingValue(item, "_id"); id != nil && id.Value == refID {
			return item
		}

		if children := mappingValue(item, "_has"); children != nil {
			if found := findInSequence(children, refID); found != nil {
				return found
			}
		}
	}

	return nil
}

// mappingValue returns the value node for a key in a mapping, or nil.
func mappingValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}

	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}

	return nil
}

// retypeScalar writes a new value into a scalar node, keeping the author's
// typing where the new value still fits it.
//
// A zip code declared as "01234" must not come back as the integer 1234, and an
// integer must not come back quoted. Keeping the original tag unless the value
// no longer fits it does both.
func retypeScalar(node *yaml.Node, value string) {
	node.Kind = yaml.ScalarNode

	if value == "NULL" {
		node.Tag = "!!null"
		node.Value = "null"
		node.Style = 0
		return
	}

	switch node.Tag {
	case "!!int":
		if _, err := strconv.ParseInt(value, 10, 64); err == nil {
			node.Value = value
			return
		}
	case "!!float":
		if _, err := strconv.ParseFloat(value, 64); err == nil {
			node.Value = value
			return
		}
	case "!!bool":
		if _, err := strconv.ParseBool(value); err == nil {
			node.Value = value
			return
		}
	case "!!str":
		node.Value = value
		return
	}

	// A tag the new value no longer fits, or one that carried no value at all
	// (a null being given a string). Let it be a string and let the encoder
	// decide whether it needs quoting.
	node.Tag = "!!str"
	node.Style = 0
	node.Value = value
}

// writeYAML re-encodes the document over the file, through a temporary file
// beside it so a failure part-way leaves the original intact.
func writeYAML(path string, root *yaml.Node) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".joka-yaml-*")
	if err != nil {
		return fmt.Errorf("creating a temporary file beside %s: %w", path, err)
	}
	defer os.Remove(tmp.Name())

	enc := yaml.NewEncoder(tmp)
	enc.SetIndent(2)

	if err := enc.Encode(root); err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	if err := enc.Close(); err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("encoding %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close() //nolint:errcheck
		return fmt.Errorf("flushing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}

	// Keep whatever mode the file already had; a fresh temp file is 0600.
	if info, err := os.Stat(path); err == nil {
		if err := os.Chmod(tmp.Name(), info.Mode().Perm()); err != nil {
			return fmt.Errorf("setting the mode on %s: %w", path, err)
		}
	}

	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}

	return nil
}
