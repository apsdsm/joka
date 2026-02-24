package infra

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DiscoverEntityFiles recursively walks the given directory and returns
// relative paths of all .yaml and .yml files, sorted by filepath.Walk's
// natural order (lexicographic). The returned paths are relative to dir.
func DiscoverEntityFiles(dir string) ([]string, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("entities directory not found: %s", dir)
	}

	var files []string

	err = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))

		if ext != ".yaml" && ext != ".yml" {
			return nil
		}

		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}

		files = append(files, rel)

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walking entities directory: %w", err)
	}

	return files, nil
}
