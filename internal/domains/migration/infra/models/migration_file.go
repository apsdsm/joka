package models

// MigrationFile represents a SQL migration file discovered on disk.
// The Index and Name are parsed from the filename pattern YYMMDDHHMMSS_name.sql.
type MigrationFile struct {
	Index    string // timestamp prefix extracted from the filename
	Name     string // descriptive name extracted from the filename
	FullPath string // absolute path to the .sql file
}
