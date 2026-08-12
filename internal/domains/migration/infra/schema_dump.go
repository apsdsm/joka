package infra

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"

	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/domains/migration/domain"
)

// SchemaDumper produces the live database schema as SQL using the database's own
// dump tool. Joka deliberately does not reconstruct DDL itself — pg_dump is the
// reference implementation, and hand-rolled reconstruction silently loses
// whatever it does not know about.
//
// PostgreSQL only for now. MySQL is a separate problem: mysqldump writes views,
// routines and triggers using `DELIMITER` (a mysql-client directive, not SQL) and
// multi-line `/*!NNNNN ... */` conditional blocks, none of which
// db.SplitSQLStatements can run. That needs work in the splitter, and is worth
// doing only once this flow has proven itself on Postgres.
type SchemaDumper interface {
	// Tool is the external binary this dumper shells out to.
	Tool() string
	// Dump returns schema SQL that joka's own applier can run, with the joka
	// tracking tables excluded.
	Dump(ctx context.Context) (string, error)
}

// NewSchemaDumper returns the dumper for the given driver, or
// domain.ErrDumpDriverUnsupported if joka cannot build a baseline for it yet.
func NewSchemaDumper(driver jokadb.Driver, conn *sql.DB, dsn string) (SchemaDumper, error) {
	if driver != jokadb.Postgres {
		return nil, fmt.Errorf("%w: %s", domain.ErrDumpDriverUnsupported, driver)
	}
	return PgSchemaDumper{conn: conn, dsn: dsn}, nil
}

// PgSchemaDumper dumps a PostgreSQL schema with pg_dump.
type PgSchemaDumper struct {
	conn *sql.DB
	dsn  string
}

// Tool implements SchemaDumper.
func (d PgSchemaDumper) Tool() string { return "pg_dump" }

// Dump runs pg_dump --schema-only and returns SQL joka can apply. Everything
// joka's own reconstruction could not represent — types, domains, views,
// materialized views, functions, triggers, extensions, identity and generated
// columns — comes across, and foreign keys arrive as trailing ALTER TABLEs, so
// table order does not matter.
func (d PgSchemaDumper) Dump(ctx context.Context) (string, error) {
	u, err := url.Parse(d.dsn)
	if err != nil {
		return "", fmt.Errorf("parsing connection URL: %w", err)
	}

	env := os.Environ()
	if password, ok := u.User.Password(); ok {
		// Keep the password out of the argument list, where any user on the box
		// could read it out of `ps`.
		u.User = url.User(u.User.Username())
		env = append(env, "PGPASSWORD="+password)
	}

	// The wildcard also excludes the sequences owned by the joka tables, which
	// naming each table individually would leave behind.
	args := []string{
		"--schema-only",
		"--no-owner",
		"--no-privileges",
		"--exclude-table=joka_*",
		"--dbname=" + u.String(),
	}

	out, err := runDumpTool(ctx, "pg_dump", args, env)
	if err != nil {
		return "", err
	}

	script := stripPsqlMetaCommands(out)
	if err := verifyTableCoverage(ctx, d.conn, script); err != nil {
		return "", err
	}
	return script, nil
}

// runDumpTool executes a dump binary and returns its stdout.
func runDumpTool(ctx context.Context, tool string, args, env []string) (string, error) {
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Env = env

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", fmt.Errorf("%w: %s is not installed or not on PATH", domain.ErrDumpToolMissing, tool)
		}
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return "", fmt.Errorf("%s failed: %s", tool, message)
		}
		return "", fmt.Errorf("%s failed: %w", tool, err)
	}

	if strings.TrimSpace(stdout.String()) == "" {
		return "", fmt.Errorf("%s produced no output", tool)
	}
	return stdout.String(), nil
}

// stripPsqlMetaCommands removes lines that are psql client directives rather
// than SQL. Recent pg_dump releases wrap their output in `\restrict` /
// `\unrestrict`, which the server rejects.
func stripPsqlMetaCommands(script string) string {
	lines := strings.Split(script, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), `\`) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// createTablePattern matches a CREATE TABLE at the start of a line, which is how
// pg_dump formats them.
var createTablePattern = regexp.MustCompile(`(?mi)^CREATE TABLE `)

// verifyTableCoverage checks the dump contains one CREATE TABLE per table in the
// database. It catches a truncated dump or a mistake in the joka_* exclusions —
// the kind of thing that would otherwise only surface when someone rebuilds from
// the baseline.
func verifyTableCoverage(ctx context.Context, conn *sql.DB, script string) error {
	var want int
	err := conn.QueryRowContext(ctx, `
		SELECT count(*)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('r', 'p')
		AND n.nspname NOT IN ('pg_catalog', 'information_schema')
		AND NOT n.nspname LIKE 'pg_toast%'
		AND c.relname NOT LIKE 'joka\_%' ESCAPE '\'
	`).Scan(&want)
	if err != nil {
		return fmt.Errorf("counting tables: %w", err)
	}

	got := len(createTablePattern.FindAllString(script, -1))
	if got != want {
		return fmt.Errorf("%w: the dump holds %d CREATE TABLE statements but the database has %d tables",
			domain.ErrDumpNotApplicable, got, want)
	}
	return nil
}
