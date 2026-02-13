package infra

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/apsdsm/joka/internal/domains/template/domain"
	"gopkg.in/yaml.v3"
)

type TableConfig struct {
	Name     string
	Strategy domain.StrategyType
}

func GetTables(templatesDir string, tableConfigs []TableConfig) ([]domain.Table, error) {
	info, err := os.Stat(templatesDir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("templates directory not found: %s", templatesDir)
	}

	var tables []domain.Table
	for _, tc := range tableConfigs {
		tablePath := filepath.Join(templatesDir, tc.Name)
		tableInfo, err := os.Stat(tablePath)
		if err != nil || !tableInfo.IsDir() {
			return nil, fmt.Errorf("table directory not found: %s", tablePath)
		}

		entries, err := os.ReadDir(tablePath)
		if err != nil {
			return nil, fmt.Errorf("reading table directory: %w", err)
		}

		var records []domain.Record
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			ext := strings.ToLower(filepath.Ext(entry.Name()))
			var recordType domain.RecordType
			switch ext {
			case ".csv":
				recordType = domain.RecordTypeList
			case ".yaml", ".yml":
				recordType = domain.RecordTypeRow
			default:
				continue
			}

			stem := strings.TrimSuffix(entry.Name(), ext)
			records = append(records, domain.Record{
				Name: stem,
				Path: filepath.Join(tablePath, entry.Name()),
				Type: recordType,
			})
		}

		strategy := tc.Strategy
		if strategy == "" {
			strategy = domain.StrategyUpdate
		}

		tables = append(tables, domain.Table{
			Name:     tc.Name,
			Path:     tablePath,
			Strategy: strategy,
			Records:  records,
		})
	}

	return tables, nil
}

func LoadRecord(record domain.Record) ([]map[string]any, error) {
	switch record.Type {
	case domain.RecordTypeRow:
		data, err := os.ReadFile(record.Path)
		if err != nil {
			return nil, fmt.Errorf("reading record file %s: %w", record.Path, err)
		}
		var row map[string]any
		if err := yaml.Unmarshal(data, &row); err != nil {
			return nil, fmt.Errorf("parsing YAML record %s: %w", record.Path, err)
		}
		if row == nil {
			return nil, nil
		}
		return []map[string]any{row}, nil

	case domain.RecordTypeList:
		f, err := os.Open(record.Path)
		if err != nil {
			return nil, fmt.Errorf("opening record file %s: %w", record.Path, err)
		}
		defer f.Close()

		reader := csv.NewReader(f)
		headers, err := reader.Read()
		if err != nil {
			return nil, fmt.Errorf("reading CSV headers from %s: %w", record.Path, err)
		}

		var rows []map[string]any
		for {
			record, err := reader.Read()
			if err != nil {
				break
			}
			row := make(map[string]any, len(headers))
			for i, h := range headers {
				if i < len(record) {
					row[h] = record[i]
				}
			}
			rows = append(rows, row)
		}
		return rows, nil
	}

	return nil, nil
}
