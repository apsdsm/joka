package infra

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/domains/migration/domain"
	"github.com/apsdsm/joka/internal/domains/migration/infra/models"
)

// PostgresDBAdapter implements the app.DBAdapter interface for PostgreSQL databases.
type PostgresDBAdapter struct {
	db     DBTX
	conn   *sql.DB
	driver jokadb.Driver
}

// NewPostgresDBAdapter creates a new PostgresDBAdapter using a direct database connection.
func NewPostgresDBAdapter(conn *sql.DB) *PostgresDBAdapter {
	return &PostgresDBAdapter{db: conn, conn: conn, driver: jokadb.Postgres}
}

// NewPostgresTxDBAdapter creates a new PostgresDBAdapter using a transaction.
func NewPostgresTxDBAdapter(tx *sql.Tx, conn *sql.DB) *PostgresDBAdapter {
	return &PostgresDBAdapter{db: tx, conn: conn, driver: jokadb.Postgres}
}

// GetAppliedMigrations retrieves the list of applied migrations from the database.
func (p *PostgresDBAdapter) GetAppliedMigrations(ctx context.Context) ([]models.MigrationRow, error) {
	exists, err := p.HasMigrationsTable(ctx)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, domain.ErrNoMigrationTable
	}

	rows, err := p.db.QueryContext(ctx, `SELECT id, migration_index, applied_at FROM joka_migrations ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var migrations []models.MigrationRow
	for rows.Next() {
		var mr models.MigrationRow
		if err := rows.Scan(&mr.ID, &mr.MigrationIndex, &mr.AppliedAt); err != nil {
			return nil, err
		}
		migrations = append(migrations, mr)
	}
	return migrations, rows.Err()
}

// ApplySQLFromFile reads and executes the SQL statements from the specified file.
func (p *PostgresDBAdapter) ApplySQLFromFile(ctx context.Context, filePath string) error {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("reading migration file: %w", err)
	}

	sqlContent := string(content)
	if strings.TrimSpace(sqlContent) == "" {
		return nil
	}

	for _, stmt := range jokadb.SplitSQLStatements(sqlContent) {
		if _, err := p.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

// RecordMigrationApplied records a migration as applied in the migrations table.
func (p *PostgresDBAdapter) RecordMigrationApplied(ctx context.Context, migrationIndex string) error {
	_, err := p.db.ExecContext(ctx, `INSERT INTO joka_migrations (migration_index) VALUES ($1)`, migrationIndex)
	return err
}

// HasMigrationsTable checks if the migrations table exists in the database.
func (p *PostgresDBAdapter) HasMigrationsTable(ctx context.Context) (bool, error) {
	return jokadb.TableExists(ctx, p.conn, p.driver, "joka_migrations")
}

// CreateMigrationsTable creates the migrations table if it does not already exist.
func (p *PostgresDBAdapter) CreateMigrationsTable(ctx context.Context) error {
	exists, err := p.HasMigrationsTable(ctx)
	if err != nil {
		return err
	}
	if exists {
		return domain.ErrMigrationAlreadyExists
	}

	_, err = p.conn.ExecContext(ctx, `
		CREATE TABLE joka_migrations (
			id SERIAL PRIMARY KEY,
			migration_index VARCHAR(255) NOT NULL UNIQUE,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	if err != nil {
		return fmt.Errorf("%w: %v", domain.ErrMigrationTableCreation, err)
	}
	return nil
}

// EnsureSnapshotsTable creates the joka_snapshots table if it doesn't already exist.
func (p *PostgresDBAdapter) EnsureSnapshotsTable(ctx context.Context) error {
	exists, err := jokadb.TableExists(ctx, p.conn, p.driver, "joka_snapshots")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	_, err = p.conn.ExecContext(ctx, `
		CREATE TABLE joka_snapshots (
			id SERIAL PRIMARY KEY,
			migration_index VARCHAR(255) NOT NULL UNIQUE,
			schema_snapshot TEXT NOT NULL,
			captured_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	return err
}

// ComputeSchema returns the current database schema as a map of table name to
// a reconstructed CREATE TABLE-like statement, for all non-joka user tables.
//
// It reads through p.db (the migration transaction during `migrate up`), NOT
// p.conn (the pool). This is load-bearing: a migration that ALTERs/DROPs an
// existing table holds ACCESS EXCLUSIVE on it inside the open tx; introspecting
// that table on a second (pool) connection would block on that lock while the
// tx waits for the snapshot to finish — an unbreakable cross-connection
// deadlock. Reading on the same tx connection avoids it (and correctly sees the
// uncommitted in-tx schema).
// Only ordinary tables are captured: views, materialized views, foreign tables
// and partitioned tables cannot be faithfully reconstructed here, and emitting
// them as plain CREATE TABLE would produce a wrong schema. UnsupportedSchemaObjects
// reports them so consolidation can refuse rather than silently drop them.
func (p *PostgresDBAdapter) ComputeSchema(ctx context.Context) (map[string]string, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT c.relname, quote_ident(c.relname)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema()
		AND c.relkind = 'r'
		AND c.relname NOT LIKE 'joka\_%' ESCAPE '\'
		ORDER BY c.relname
	`)
	if err != nil {
		return nil, fmt.Errorf("listing tables: %w", err)
	}
	defer rows.Close()

	var tableNames, quotedNames []string
	for rows.Next() {
		var name, quoted string
		if err := rows.Scan(&name, &quoted); err != nil {
			return nil, err
		}
		tableNames = append(tableNames, name)
		quotedNames = append(quotedNames, quoted)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	schema := make(map[string]string)
	for i, name := range tableNames {
		stmt, err := p.reconstructCreateTable(ctx, name, quotedNames[i])
		if err != nil {
			return nil, fmt.Errorf("getting schema for table %s: %w", name, err)
		}
		schema[name] = stmt
	}
	return schema, nil
}

// CaptureSchemaSnapshot captures the current database schema and stores it
// associated with the given migration index.
func (p *PostgresDBAdapter) CaptureSchemaSnapshot(ctx context.Context, migrationIndex string) error {
	if err := p.EnsureSnapshotsTable(ctx); err != nil {
		return fmt.Errorf("ensuring snapshots table: %w", err)
	}

	schema, err := p.ComputeSchema(ctx)
	if err != nil {
		return err
	}

	jsonBytes, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("marshaling schema: %w", err)
	}

	_, err = p.db.ExecContext(ctx,
		`INSERT INTO joka_snapshots (migration_index, schema_snapshot) VALUES ($1, $2)`,
		migrationIndex, string(jsonBytes),
	)
	return err
}

// serialTypes maps the integer type a serial column resolves to onto the
// serial pseudo-type that recreates both the column and its owned sequence.
var serialTypes = map[string]string{
	"smallint": "smallserial",
	"integer":  "serial",
	"bigint":   "bigserial",
}

// reconstructCreateTable builds a CREATE TABLE statement from pg_catalog for
// snapshot purposes. Includes columns, primary keys, unique constraints,
// foreign keys, and indexes.
//
// Column types come from format_type(), which renders arrays (text[]),
// parameterised types (numeric(10,2), character varying(20)) and user-defined
// types correctly in one step — information_schema.columns.data_type reports
// the useless placeholders 'ARRAY' and 'USER-DEFINED' for those. Identity and
// generated-column flags are read from pg_attribute; neither is a column
// default, so neither survives an information_schema.columns round trip.
func (p *PostgresDBAdapter) reconstructCreateTable(ctx context.Context, tableName, quotedName string) (string, error) {
	// 1. Columns
	colRows, err := p.db.QueryContext(ctx, `
		SELECT
			quote_ident(a.attname),
			format_type(a.atttypid, a.atttypmod),
			a.attnotnull,
			pg_get_expr(d.adbin, d.adrelid),
			a.attidentity::text,
			a.attgenerated::text,
			pg_get_serial_sequence(
				quote_ident(n.nspname) || '.' || quote_ident(c.relname), a.attname
			) IS NOT NULL
		FROM pg_attribute a
		JOIN pg_class c ON c.oid = a.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
		WHERE n.nspname = current_schema()
		AND c.relname = $1
		AND a.attnum > 0
		AND NOT a.attisdropped
		ORDER BY a.attnum
	`, tableName)
	if err != nil {
		return "", err
	}
	defer colRows.Close()

	var parts []string
	for colRows.Next() {
		var colName, colType, identity, generated string
		var notNull, ownsSequence bool
		var colDefault sql.NullString

		if err := colRows.Scan(&colName, &colType, &notNull, &colDefault, &identity, &generated, &ownsSequence); err != nil {
			return "", err
		}

		// A serial column is an integer column whose default is nextval() on a
		// sequence it owns. Rendering it as `serial` recreates that sequence;
		// copying the nextval() default alone would reference a sequence that
		// the consolidated file never creates.
		isSerial := identity == "" && ownsSequence && colDefault.Valid &&
			strings.HasPrefix(colDefault.String, "nextval(")
		if isSerial {
			if serial, ok := serialTypes[colType]; ok {
				colType = serial
			} else {
				isSerial = false
			}
		}

		col := fmt.Sprintf("  %s %s", colName, colType)
		if notNull {
			col += " NOT NULL"
		}
		switch {
		case identity == "a":
			col += " GENERATED ALWAYS AS IDENTITY"
		case identity == "d":
			col += " GENERATED BY DEFAULT AS IDENTITY"
		case generated == "s" && colDefault.Valid:
			col += fmt.Sprintf(" GENERATED ALWAYS AS (%s) STORED", colDefault.String)
		case !isSerial && colDefault.Valid:
			col += fmt.Sprintf(" DEFAULT %s", colDefault.String)
		}
		parts = append(parts, col)
	}
	if err := colRows.Err(); err != nil {
		return "", err
	}

	// 2. Table constraints (primary keys, unique, foreign keys)
	conRows, err := p.db.QueryContext(ctx, `
		SELECT
			c.conname,
			c.contype,
			pg_get_constraintdef(c.oid, true) AS def
		FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE t.relname = $1
		AND n.nspname = current_schema()
		ORDER BY
			CASE c.contype WHEN 'p' THEN 0 WHEN 'u' THEN 1 WHEN 'f' THEN 2 ELSE 3 END,
			c.conname
	`, tableName)
	if err != nil {
		return "", err
	}
	defer conRows.Close()

	for conRows.Next() {
		var conName, conType, conDef string
		if err := conRows.Scan(&conName, &conType, &conDef); err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("  CONSTRAINT %s %s", conName, conDef))
	}
	if err := conRows.Err(); err != nil {
		return "", err
	}

	// Terminate here: the reconstruction may append further statements (indexes)
	// below, and every statement it emits has to stand on its own.
	result := fmt.Sprintf("CREATE TABLE %s (\n%s\n);", quotedName, strings.Join(parts, ",\n"))

	// 3. Indexes (exclude those backing constraints — already covered above).
	// pg_indexes.indexdef always schema-qualifies the table; strip that so the
	// statement targets whatever schema it is applied to.
	idxRows, err := p.db.QueryContext(ctx, `
		SELECT indexname, replace(indexdef, ' ON ' || quote_ident(schemaname) || '.', ' ON ')
		FROM pg_indexes
		WHERE schemaname = current_schema()
		AND tablename = $1
		AND indexname NOT IN (
			SELECT conname FROM pg_constraint c
			JOIN pg_class t ON t.oid = c.conrelid
			JOIN pg_namespace n ON n.oid = t.relnamespace
			WHERE t.relname = $1 AND n.nspname = current_schema()
		)
		ORDER BY indexname
	`, tableName)
	if err != nil {
		return "", err
	}
	defer idxRows.Close()

	for idxRows.Next() {
		var idxName, idxDef string
		if err := idxRows.Scan(&idxName, &idxDef); err != nil {
			return "", err
		}
		result += fmt.Sprintf("\n%s;", idxDef)
	}
	if err := idxRows.Err(); err != nil {
		return "", err
	}

	return result, nil
}

// UnsupportedSchemaObjects lists schema objects that ComputeSchema cannot
// represent — views, materialized views, foreign and partitioned tables,
// standalone sequences, user-defined types and domains, functions, procedures,
// triggers, and extensions installed into the current schema. Sequences owned
// by a serial/identity column are excluded: those are recreated with their
// column.
//
// Callers that turn a snapshot back into DDL (consolidate) must refuse when
// this is non-empty; the alternative is a baseline that is quietly missing
// objects the real schema has.
func (p *PostgresDBAdapter) UnsupportedSchemaObjects(ctx context.Context) ([]string, error) {
	rows, err := p.db.QueryContext(ctx, `
		SELECT description FROM (
			SELECT CASE c.relkind
			         WHEN 'v' THEN 'view '
			         WHEN 'm' THEN 'materialized view '
			         WHEN 'f' THEN 'foreign table '
			         WHEN 'p' THEN 'partitioned table '
			       END || quote_ident(c.relname) AS description
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = current_schema()
			AND c.relkind IN ('v', 'm', 'f', 'p')

			UNION ALL

			SELECT 'sequence ' || quote_ident(c.relname)
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = current_schema()
			AND c.relkind = 'S'
			AND NOT EXISTS (
				SELECT 1 FROM pg_depend d
				WHERE d.objid = c.oid
				AND d.classid = 'pg_class'::regclass
				AND d.deptype IN ('a', 'i')
			)

			UNION ALL

			SELECT CASE t.typtype
			         WHEN 'e' THEN 'enum type '
			         WHEN 'c' THEN 'composite type '
			         WHEN 'd' THEN 'domain '
			         WHEN 'r' THEN 'range type '
			       END || quote_ident(t.typname)
			FROM pg_type t
			JOIN pg_namespace n ON n.oid = t.typnamespace
			LEFT JOIN pg_class c ON c.oid = t.typrelid
			WHERE n.nspname = current_schema()
			AND t.typtype IN ('e', 'c', 'd', 'r')
			AND (t.typrelid = 0 OR c.relkind = 'c')
			AND NOT EXISTS (
				SELECT 1 FROM pg_depend d
				WHERE d.objid = t.oid AND d.classid = 'pg_type'::regclass AND d.deptype = 'e'
			)

			UNION ALL

			SELECT CASE p.prokind
			         WHEN 'p' THEN 'procedure '
			         WHEN 'a' THEN 'aggregate '
			         ELSE 'function '
			       END || quote_ident(p.proname)
			FROM pg_proc p
			JOIN pg_namespace n ON n.oid = p.pronamespace
			WHERE n.nspname = current_schema()
			AND NOT EXISTS (
				SELECT 1 FROM pg_depend d
				WHERE d.objid = p.oid AND d.classid = 'pg_proc'::regclass AND d.deptype = 'e'
			)

			UNION ALL

			SELECT 'trigger ' || quote_ident(tg.tgname) || ' on ' || quote_ident(c.relname)
			FROM pg_trigger tg
			JOIN pg_class c ON c.oid = tg.tgrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = current_schema()
			AND NOT tg.tgisinternal
			AND c.relname NOT LIKE 'joka\_%' ESCAPE '\'

			UNION ALL

			SELECT 'extension ' || quote_ident(e.extname)
			FROM pg_extension e
			JOIN pg_namespace n ON n.oid = e.extnamespace
			WHERE n.nspname = current_schema()
		) objects
		ORDER BY description
	`)
	if err != nil {
		return nil, fmt.Errorf("listing unsupported schema objects: %w", err)
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

// consolidateCheckSchema is the throwaway schema ValidateSchemaSQL applies
// candidate DDL into. It only ever exists inside a rolled-back transaction.
const consolidateCheckSchema = "joka_consolidate_check"

// ValidateSchemaSQL applies the given SQL to a scratch schema inside a
// transaction that is always rolled back, so a generated schema is proven
// applicable before anything irreversible is done with it. The database is left
// untouched either way.
func (p *PostgresDBAdapter) ValidateSchemaSQL(ctx context.Context, script string) error {
	tx, err := p.conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("starting validation transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck — the rollback is the point; nothing is ever committed

	if _, err := tx.ExecContext(ctx, `CREATE SCHEMA `+consolidateCheckSchema); err != nil {
		return fmt.Errorf("creating validation schema (needs CREATE on the database): %w", err)
	}
	// Scratch schema only — no public fallback, so a statement can't be
	// satisfied by an existing table of the same name in the real schema.
	if _, err := tx.ExecContext(ctx, `SET LOCAL search_path = `+consolidateCheckSchema); err != nil {
		return fmt.Errorf("setting validation search_path: %w", err)
	}

	for _, stmt := range jokadb.SplitSQLStatements(script) {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("%w: %v\n\nfailing statement:\n%s", domain.ErrSchemaNotApplicable, err, strings.TrimSpace(stmt))
		}
	}
	return nil
}

// RemoveMigrationRecords deletes the given migration indexes from
// joka_migrations, along with any snapshots captured for them, in a single
// transaction. Used by consolidate to keep the bookkeeping in step with the
// files it replaced — the chain is zipped positionally, so a stale row breaks
// every subsequent command.
func (p *PostgresDBAdapter) RemoveMigrationRecords(ctx context.Context, indexes []string) error {
	if len(indexes) == 0 {
		return nil
	}
	if err := p.EnsureSnapshotsTable(ctx); err != nil {
		return fmt.Errorf("ensuring snapshots table: %w", err)
	}

	tx, err := p.conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck — no-op once committed

	for _, index := range indexes {
		if _, err := tx.ExecContext(ctx, `DELETE FROM joka_migrations WHERE migration_index = $1`, index); err != nil {
			return fmt.Errorf("removing migration record %s: %w", index, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM joka_snapshots WHERE migration_index = $1`, index); err != nil {
			return fmt.Errorf("removing snapshot for %s: %w", index, err)
		}
	}
	return tx.Commit()
}

// GetSchemaSnapshot retrieves the stored schema snapshot for a given migration index.
func (p *PostgresDBAdapter) GetSchemaSnapshot(ctx context.Context, migrationIndex string) (string, error) {
	if err := p.EnsureSnapshotsTable(ctx); err != nil {
		return "", fmt.Errorf("ensuring snapshots table: %w", err)
	}

	var snapshot string
	err := p.conn.QueryRowContext(ctx,
		`SELECT schema_snapshot FROM joka_snapshots WHERE migration_index = $1`,
		migrationIndex,
	).Scan(&snapshot)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("no snapshot found for migration %s", migrationIndex)
	}
	return snapshot, err
}

// GetLatestSnapshotIndex returns the migration index of the most recent snapshot.
func (p *PostgresDBAdapter) GetLatestSnapshotIndex(ctx context.Context) (string, error) {
	if err := p.EnsureSnapshotsTable(ctx); err != nil {
		return "", fmt.Errorf("ensuring snapshots table: %w", err)
	}

	var index string
	err := p.conn.QueryRowContext(ctx,
		`SELECT migration_index FROM joka_snapshots ORDER BY id DESC LIMIT 1`,
	).Scan(&index)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("no snapshots found")
	}
	return index, err
}
