package infra

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/apsdsm/joka/internal/domains/template/domain"
)

func TestLoadRecord_YAMLSingleRow(t *testing.T) {
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "welcome.yaml")
	os.WriteFile(yamlFile, []byte("subject: Welcome\nbody: Hello world\n"), 0644)

	rows, err := LoadRecord(domain.Record{
		Name: "welcome",
		Path: yamlFile,
		Type: domain.RecordTypeRow,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0]["subject"] != "Welcome" {
		t.Errorf("expected subject 'Welcome', got %v", rows[0]["subject"])
	}
	if rows[0]["body"] != "Hello world" {
		t.Errorf("expected body 'Hello world', got %v", rows[0]["body"])
	}
}

func TestLoadRecord_CSVMultipleRows(t *testing.T) {
	dir := t.TempDir()
	csvFile := filepath.Join(dir, "defaults.csv")
	os.WriteFile(csvFile, []byte("key,value\ntimeout,30\nretries,3\n"), 0644)

	rows, err := LoadRecord(domain.Record{
		Name: "defaults",
		Path: csvFile,
		Type: domain.RecordTypeList,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if rows[0]["key"] != "timeout" {
		t.Errorf("expected key 'timeout', got %v", rows[0]["key"])
	}
	if rows[1]["value"] != "3" {
		t.Errorf("expected value '3', got %v", rows[1]["value"])
	}
}

func TestGetTables_ConfigParsing(t *testing.T) {
	dir := t.TempDir()

	// Create config
	configYAML := `name: test_data
tables:
  - name: emails
    strategy: truncate
  - name: settings
    strategy: truncate
`
	os.WriteFile(filepath.Join(dir, "_config.yaml"), []byte(configYAML), 0644)

	// Create table directories with files
	emailsDir := filepath.Join(dir, "emails")
	os.Mkdir(emailsDir, 0755)
	os.WriteFile(filepath.Join(emailsDir, "welcome.yaml"), []byte("subject: hi\n"), 0644)

	settingsDir := filepath.Join(dir, "settings")
	os.Mkdir(settingsDir, 0755)
	os.WriteFile(filepath.Join(settingsDir, "defaults.csv"), []byte("key,value\na,b\n"), 0644)

	tables, err := GetTables(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(tables) != 2 {
		t.Fatalf("expected 2 tables, got %d", len(tables))
	}
	if tables[0].Name != "emails" {
		t.Errorf("expected table name 'emails', got %s", tables[0].Name)
	}
	if tables[0].Strategy != domain.StrategyTruncate {
		t.Errorf("expected strategy truncate, got %s", tables[0].Strategy)
	}
	if len(tables[0].Records) != 1 {
		t.Fatalf("expected 1 record for emails, got %d", len(tables[0].Records))
	}
	if tables[0].Records[0].Type != domain.RecordTypeRow {
		t.Errorf("expected record type row, got %s", tables[0].Records[0].Type)
	}
	if tables[1].Records[0].Type != domain.RecordTypeList {
		t.Errorf("expected record type list, got %s", tables[1].Records[0].Type)
	}
}
