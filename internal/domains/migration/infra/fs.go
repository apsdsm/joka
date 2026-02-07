package infra

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/nickfiggins/joka/internal/domains/migration/infra/models"
)

// migrationPattern matches filenames of the form YYMMDDHHMMSS_name.sql.
var migrationPattern = regexp.MustCompile(`^(\d{12})_.*\.sql$`)

// ListMigrationFiles scans dir for SQL files matching the migration naming
// convention and returns them sorted by their timestamp index. Non-matching
// files and subdirectories are silently ignored.
func ListMigrationFiles(dir string) ([]models.MigrationFile, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("migrations directory not found: %s", dir)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading migrations directory: %w", err)
	}

	var files []models.MigrationFile
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		matches := migrationPattern.FindStringSubmatch(name)
		if matches == nil {
			continue
		}
		index := matches[1]
		// name is everything after the index + underscore, minus .sql
		migName := name[len(index)+1 : len(name)-4]
		fullPath, _ := filepath.Abs(filepath.Join(dir, name))

		files = append(files, models.MigrationFile{
			Index:    index,
			Name:     migName,
			FullPath: fullPath,
		})
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Index < files[j].Index
	})

	return files, nil
}

// CreateMigrationFile creates a new empty SQL migration file in dir using the
// current timestamp as a prefix. It returns the generated filename (not the
// full path). The migrations directory must already exist.
func CreateMigrationFile(dir string, name string) (string, error) {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("migrations directory not found: %s", dir)
	}

	timestamp := time.Now().Format("20060102150405")
	filename := fmt.Sprintf("%s_%s.sql", timestamp, name)
	filePath := filepath.Join(dir, filename)

	if err := os.WriteFile(filePath, []byte("-- Write your migration SQL here\n"), 0644); err != nil {
		return "", fmt.Errorf("creating migration file: %w", err)
	}

	return filename, nil
}
