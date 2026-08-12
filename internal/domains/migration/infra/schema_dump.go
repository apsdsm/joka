package infra

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/go-sql-driver/mysql"

	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/domains/migration/domain"
)

// jokaTables are the tracking tables joka owns. A consolidated baseline must not
// carry them: `joka init` creates joka_migrations and the rest are created on
// first use.
var jokaTables = []string{
	"joka_migrations",
	"joka_lock",
	"joka_snapshots",
	"joka_entities",
	"joka_entity_rows",
}

// SchemaDumper produces the live database schema as SQL using the database's own
// dump tool. Joka deliberately does not reconstruct DDL itself — pg_dump and
// mysqldump are the reference implementations, and hand-rolled reconstruction
// silently loses whatever it does not know about.
type SchemaDumper interface {
	// Tool is the external binary this dumper shells out to.
	Tool() string
	// UncarriedObjects lists schema objects the dump will not contain. Non-empty
	// means a baseline built from it would be incomplete.
	UncarriedObjects(ctx context.Context) ([]string, error)
	// Dump returns schema SQL that joka's own applier can run, with the joka
	// tracking tables excluded.
	Dump(ctx context.Context) (string, error)
}

// NewSchemaDumper returns the dumper for the given driver.
func NewSchemaDumper(driver jokadb.Driver, conn *sql.DB, dsn string) SchemaDumper {
	if driver == jokadb.Postgres {
		return PgSchemaDumper{conn: conn, dsn: dsn}
	}
	return MySQLSchemaDumper{conn: conn, dsn: dsn}
}

// PgSchemaDumper dumps a PostgreSQL schema with pg_dump.
type PgSchemaDumper struct {
	conn *sql.DB
	dsn  string
}

// Tool implements SchemaDumper.
func (d PgSchemaDumper) Tool() string { return "pg_dump" }

// UncarriedObjects returns nothing: pg_dump captures every object class joka can
// encounter — types, domains, functions, views, materialized views, sequences,
// triggers, extensions and partitioned tables all come across.
func (d PgSchemaDumper) UncarriedObjects(ctx context.Context) ([]string, error) {
	return nil, nil
}

// Dump runs pg_dump --schema-only and returns SQL joka can apply.
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
	if err := verifyTableCoverage(ctx, d.conn, jokadb.Postgres, script); err != nil {
		return "", err
	}
	return script, nil
}

// MySQLSchemaDumper dumps a MySQL schema with mysqldump.
//
// Only tables are dumped. mysqldump emits views, routines and triggers wrapped
// in constructs joka's applier cannot run — `DELIMITER`, which is a mysql-client
// directive rather than SQL, and multi-line `/*!NNNNN ... */` version-conditional
// blocks. UncarriedObjects reports them so consolidation can refuse instead.
type MySQLSchemaDumper struct {
	conn *sql.DB
	dsn  string
}

// Tool implements SchemaDumper.
func (d MySQLSchemaDumper) Tool() string { return "mysqldump" }

// UncarriedObjects lists the views, routines, triggers and events a tables-only
// dump leaves out.
func (d MySQLSchemaDumper) UncarriedObjects(ctx context.Context) ([]string, error) {
	rows, err := d.conn.QueryContext(ctx, `
		SELECT description FROM (
			SELECT CONCAT('view ', table_name) AS description
			FROM information_schema.views
			WHERE table_schema = DATABASE()

			UNION ALL

			SELECT CONCAT(LOWER(routine_type), ' ', routine_name)
			FROM information_schema.routines
			WHERE routine_schema = DATABASE()

			UNION ALL

			SELECT CONCAT('trigger ', trigger_name)
			FROM information_schema.triggers
			WHERE trigger_schema = DATABASE()

			UNION ALL

			SELECT CONCAT('event ', event_name)
			FROM information_schema.events
			WHERE event_schema = DATABASE()
		) objects
		ORDER BY description
	`)
	if err != nil {
		return nil, fmt.Errorf("listing schema objects: %w", err)
	}
	defer rows.Close()

	var objects []string
	for rows.Next() {
		var description string
		if err := rows.Scan(&description); err != nil {
			return nil, err
		}
		objects = append(objects, description)
	}
	return objects, rows.Err()
}

// Dump runs mysqldump for table structures only and returns SQL joka can apply.
func (d MySQLSchemaDumper) Dump(ctx context.Context) (string, error) {
	cfg, err := mysql.ParseDSN(d.dsn)
	if err != nil {
		return "", fmt.Errorf("parsing DSN: %w", err)
	}
	if cfg.DBName == "" {
		return "", fmt.Errorf("connection DSN names no database")
	}

	args, err := mysqldumpConnArgs(cfg)
	if err != nil {
		return "", err
	}
	args = append(args,
		"--no-data",
		"--skip-triggers",
		"--skip-add-drop-table",
		"--skip-dump-date",
	)
	for _, table := range jokaTables {
		args = append(args, "--ignore-table="+cfg.DBName+"."+table)
	}
	args = append(args, cfg.DBName)

	env := os.Environ()
	if cfg.Passwd != "" {
		// MYSQL_PWD rather than --password, which would expose it in `ps`.
		env = append(env, "MYSQL_PWD="+cfg.Passwd)
	}

	out, err := runDumpTool(ctx, "mysqldump", args, env)
	if err != nil {
		return "", err
	}

	script, err := stripMySQLConditionalStatements(out)
	if err != nil {
		return "", err
	}
	// mysqldump emits tables alphabetically and relies on the client disabling
	// foreign key checks, which it does with a version-conditional statement
	// that joka's splitter treats as a comment. Say it in plain SQL instead.
	script = "SET FOREIGN_KEY_CHECKS = 0;\n\n" + script + "\nSET FOREIGN_KEY_CHECKS = 1;\n"

	if err := verifyTableCoverage(ctx, d.conn, jokadb.MySQL, script); err != nil {
		return "", err
	}
	return script, nil
}

// mysqldumpConnArgs translates a parsed MySQL DSN into mysqldump connection
// flags. The password is passed via the environment, not here.
func mysqldumpConnArgs(cfg *mysql.Config) ([]string, error) {
	args := []string{}

	switch cfg.Net {
	case "unix":
		args = append(args, "--protocol=SOCKET", "--socket="+cfg.Addr)
	case "tcp", "":
		host, port, err := net.SplitHostPort(cfg.Addr)
		if err != nil {
			return nil, fmt.Errorf("parsing address %q: %w", cfg.Addr, err)
		}
		args = append(args, "--protocol=TCP", "--host="+host, "--port="+port)
	default:
		return nil, fmt.Errorf("cannot translate DSN network %q for mysqldump", cfg.Net)
	}

	if cfg.User != "" {
		args = append(args, "--user="+cfg.User)
	}

	switch cfg.TLSConfig {
	case "", "false":
	case "true":
		args = append(args, "--ssl-mode=VERIFY_IDENTITY")
	case "skip-verify":
		args = append(args, "--ssl-mode=REQUIRED")
	case "preferred":
		args = append(args, "--ssl-mode=PREFERRED")
	default:
		return nil, fmt.Errorf("cannot translate custom TLS config %q for mysqldump", cfg.TLSConfig)
	}

	return args, nil
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

// mysqlConditionalStatement matches a whole-line `/*!NNNNN ... */;` statement.
var mysqlConditionalStatement = regexp.MustCompile(`^\s*/\*!\d*\s.*\*/;\s*$`)

// stripMySQLConditionalStatements removes mysqldump's single-line
// version-conditional statements (client charset save/restore, key and FK check
// toggles). Joka's SQL splitter classifies them as comments and drops them
// anyway; removing them here keeps the written file honest about what will run.
//
// A conditional block spanning several lines means the dump contains a view,
// routine or trigger, which the tables-only dump was supposed to exclude. Rather
// than mangle it, this refuses.
func stripMySQLConditionalStatements(script string) (string, error) {
	lines := strings.Split(script, "\n")
	kept := make([]string, 0, len(lines))

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "/*!") {
			kept = append(kept, line)
			continue
		}
		if mysqlConditionalStatement.MatchString(line) {
			continue
		}
		return "", fmt.Errorf("%w: mysqldump emitted a multi-line conditional block at line %d that joka cannot apply:\n  %s",
			domain.ErrDumpNotApplicable, i+1, trimmed)
	}

	return strings.Join(kept, "\n"), nil
}

// createTablePattern matches a CREATE TABLE at the start of a line, which is how
// both dump tools format them.
var createTablePattern = regexp.MustCompile(`(?mi)^CREATE TABLE `)

// verifyTableCoverage checks the dump contains one CREATE TABLE per table in the
// database. It catches a truncated dump or a mistake in the joka_* exclusions —
// the kind of thing that would otherwise only surface when someone rebuilds from
// the baseline.
func verifyTableCoverage(ctx context.Context, conn *sql.DB, driver jokadb.Driver, script string) error {
	query := `
		SELECT count(*)
		FROM information_schema.tables
		WHERE table_schema = DATABASE()
		AND table_type = 'BASE TABLE'
		AND table_name NOT LIKE 'joka\_%'`
	if driver == jokadb.Postgres {
		query = `
			SELECT count(*)
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relkind IN ('r', 'p')
			AND n.nspname NOT IN ('pg_catalog', 'information_schema')
			AND NOT n.nspname LIKE 'pg_toast%'
			AND c.relname NOT LIKE 'joka\_%' ESCAPE '\'`
	}

	var want int
	if err := conn.QueryRowContext(ctx, query).Scan(&want); err != nil {
		return fmt.Errorf("counting tables: %w", err)
	}

	got := len(createTablePattern.FindAllString(script, -1))
	if got != want {
		return fmt.Errorf("%w: the dump holds %d CREATE TABLE statements but the database has %d tables",
			domain.ErrDumpNotApplicable, got, want)
	}
	return nil
}
