# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Joka is a database migration and data management tool written in Go. It supports **MySQL** and **PostgreSQL**. It tracks and applies SQL migrations using a `joka_migrations` table, captures schema snapshots after each migration, and syncs seed data from files to database tables.

Module path: `github.com/apsdsm/joka`

## Development Commands

```bash
# Build
go build ./...

# Run commands during development
go run . [command] [options]

# Examples
go run . init
go run . make "add_users_table"
go run . migrate up
go run . migrate status
go run . migrate snapshot
go run . migrate verify
go run . data sync
go run . entity sync
go run . entity status
go run . entity reimport admin_user.yaml
go run . entity update admin_user.yaml
go run . drop
go run . reset
go run . unlock

# Run tests
go test ./... -v

# Run a single test
go test ./internal/domains/migration/app/ -run TestGetMigrationChain_AllApplied -v
```

## Architecture

The codebase follows a domain-driven layered architecture. Each domain lives under `internal/domains/` and has its own `domain_spec.md` with detailed documentation.

### Top-level structure

- **`main.go`** — CLI entry point using Cobra. Wires up commands, flags, and DB connection lifecycle.
- **`db/`** — Database utilities (`Open`, `TableExists`).
- **`cmd/`** — Command handlers. Each receives dependencies and calls into domain actions.
- **`internal/domains/`** — Domain logic, organized by bounded context.

### Domains

- **`migration/`** — Migration lifecycle: create files, track applied migrations, apply pending ones, capture schema snapshots.
- **`lock/`** — DB-backed advisory locking via `joka_lock` table. Prevents concurrent mutating operations.
- **`template/`** — Syncs seed/reference data from YAML/CSV files to database tables.
- **`entity/`** — Syncs entity graphs (parent-child seed data) from YAML files with reference resolution.

### Layer pattern (within each domain)

- **`domain/`** — Pure types, constants, and error sentinels. No infrastructure dependencies.
- **`app/`** — Use-case actions and interfaces (e.g. `DBAdapter`). Depends on domain types, not on specific databases.
- **`infra/`** — MySQL, PostgreSQL, and filesystem implementations. Implements the interfaces defined in `app/`. Each database has its own adapter file (`mysql.go`, `postgres.go`).
- **`infra/models/`** — Flat structs for DB rows and file representations.

## Versioning

The version is defined as a `const` in `main.go`. When bumping the version:
1. Update the `version` constant in `main.go`
2. Create a git tag matching the version (e.g. `git tag v0.3.0`)
3. Push the tag (e.g. `git push origin v0.3.0`)

## Key Technical Details

- **Go 1.25+** with `github.com/go-sql-driver/mysql` and `github.com/lib/pq`
- **Driver auto-detection**: The database driver is detected from the `DATABASE_URL` format. PostgreSQL DSNs start with `postgres://` or `postgresql://`; everything else is assumed MySQL.
- **Multi-statement SQL**: MySQL DSN is configured with `multiStatements=true`; PostgreSQL handles multiple statements natively.
- **Connection**: by default the DSN comes from `DATABASE_URL` (`.env` or environment). The
  `.jokarc.yaml` may instead declare a `connection:` block (`internal/connection`) whose `source`
  is `env`, `literal` (inline `url:`/`password:`), or `aws_secrets_manager` (assemble from parts +
  a secret key, or a whole-URL key). See README for the schema.
  - MySQL: `user:pass@tcp(host:port)/dbname`
  - PostgreSQL: `postgresql://user:pass@host:port/dbname?sslmode=disable`
- **Profiles**: `.jokarc.yaml` may define a `profiles:` map; `--profile <name>` overlays a profile
  (migrations/entities/connection) onto the base config. No `--profile` uses the base.
- **Migration files**: Named `YYMMDDHHMMSS_description.sql` in `devops/migrations/` by default.
- **CLI flags**: `--env` for .env path, `--profile`/`-p` for the config profile, `--migrations` for migrations dir, `--templates` for templates dir, `--entities` for entities dir, `--auto` for auto-confirm, `--output` / `-o` for output format (`text` or `json`).
- **JSON output**: `--output json` emits a single JSON object per command (no color, no prompts). All responses include a `"status"` field (`"ok"` or `"error"`). When `--output json` is set, confirmations are auto-skipped (like `--auto`).
- **Advisory locking**: `migrate up`, `data sync`, `entity sync`, `entity reimport`, `drop`, and `reset` acquire a DB lock before running. (`reset` holds one outer lock for the whole pipeline.) Use `joka unlock` if a process crashes without releasing.

## Database Tables

All joka-owned tables use the `joka_` prefix:

```sql
-- Migration tracking
CREATE TABLE joka_migrations (
    id INT AUTO_INCREMENT PRIMARY KEY,
    migration_index VARCHAR(255) NOT NULL UNIQUE,
    applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
)

-- Advisory lock (at most one row)
CREATE TABLE joka_lock (
    id INT PRIMARY KEY DEFAULT 1,
    locked_by VARCHAR(255) NOT NULL,
    locked_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    operation VARCHAR(255) NOT NULL
)

-- Schema snapshots (one per migration)
CREATE TABLE joka_snapshots (
    id INT AUTO_INCREMENT PRIMARY KEY,
    migration_index VARCHAR(255) NOT NULL UNIQUE,
    schema_snapshot LONGTEXT NOT NULL,
    captured_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
)
```

```sql
-- Entity sync tracking
CREATE TABLE joka_entities (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    entity_file VARCHAR(512) NOT NULL UNIQUE,
    content_hash VARCHAR(64),
    synced_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
)

-- Entity row tracking (for reimport support)
CREATE TABLE joka_entity_rows (
    id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    entity_file VARCHAR(512) NOT NULL,
    table_name VARCHAR(255) NOT NULL,
    row_pk BIGINT NOT NULL,
    pk_column VARCHAR(255) NOT NULL DEFAULT 'id',
    ref_id VARCHAR(255),
    insertion_order INT NOT NULL
)
```

`joka_lock`, `joka_snapshots`, `joka_entities`, and `joka_entity_rows` are auto-created on first use. Only `joka_migrations` requires `joka init`.

## Schema drift detection

`joka migrate verify` compares the live database schema against the schema snapshot stored for the most recent applied migration. It reports tables that were added in live but missing from the snapshot, tables present in the snapshot but missing from live, and tables whose CREATE statements differ.

- Useful for catching out-of-band DDL (manual `ALTER TABLE`, columns added without a migration, etc.).
- MySQL `AUTO_INCREMENT=<n>` is stripped before comparison so row-insertion noise doesn't false-positive.
- Exit code is non-zero when drift is detected — suitable for CI gating.

## Wipe and reseed

- **`joka drop`** — drops every table in the current database/schema, including all `joka_*` tracking tables. Confirms unless `--auto`. MySQL disables FK checks for the drop; Postgres uses `DROP TABLE ... CASCADE`.
- **`joka reset`** — wipe-and-reseed pipeline: runs `drop`, then `init`, `migrate up`, `data sync`, `entity sync` in sequence. Acquires one outer advisory lock for the whole flow and confirms once.

## Templates

The `joka data sync` command syncs template/seed data from files to database tables.

**Directory structure** (`devops/templates/` by default):
```
devops/templates/
├── _config.yaml          # Defines tables and sync strategies
├── email_templates/      # Directory per table
│   ├── welcome.yaml      # YAML = single row
│   └── reminder.yaml
└── settings/
    └── defaults.csv      # CSV = multiple rows
```

**_config.yaml format**:
```yaml
name: app_data
tables:
  - name: email_templates
    strategy: truncate      # truncate | update | delete
  - name: settings
    strategy: truncate
```

**Strategies**:
- `truncate` - Delete all rows, then insert from files (implemented)
- `update` - Upsert/merge with existing data (not yet implemented)
- `delete` - (not yet implemented)

## Entities

The `joka entity sync` command syncs entity graphs from YAML files to database tables. Unlike templates, entities support parent-child relationships and cross-row references.

**Directory structure** (`devops/entities/` by default):
```
devops/entities/
├── admin_user.yaml
└── test_data.yaml
```

**Entity YAML format**:
```yaml
entities:
  - _is: users
    _id: admin
    _pk: id              # optional, defaults to "id"
    name: Admin
    email: admin@example.com
    password_hash: "{{ argon2id|admin123 }}"
    _has:
      - _is: profiles
        user_id: "{{ admin.id }}"
        bio: "System administrator"
```

**Reserved keys** (underscore-prefixed, not inserted as columns):
- `_is` (required) — Target table name
- `_id` (optional) — Reference handle for this entity's auto-generated PK
- `_pk` (optional) — Primary key column name, defaults to `"id"`. Used by PostgreSQL adapter for `RETURNING` clause; MySQL ignores it (uses `LastInsertId`)
- `_has` (optional) — List of child entities, inserted after the parent

**Template expressions** (resolved at insert time):
- `{{ now }}` — Current UTC timestamp (`2006-01-02 15:04:05`)
- `{{ <ref>.id }}` — Auto-generated PK of a previously inserted entity (looked up by `_id` handle)
- `{{ argon2id|<plaintext> }}` — Argon2id hash of the given plaintext
- `{{ sha256|<value> }}` — SHA-256 hex digest of the given value
- `{{ lookup|table,return_col,where_col=value }}` — Query a value from an existing table row (e.g. `{{ lookup|industry_types,id,code=RESTAURANT }}`). Useful for referencing rows seeded outside the entity file (via templates or migrations)

**Insertion behavior**:
- Entities are inserted depth-first: parent first, then children in order
- Each entity's auto-generated PK is stored in a reference map under its `_id` handle
- Children can reference any previously inserted entity via `{{ handle.id }}`
- All inserts within a file run in a single transaction
- Files are tracked in `joka_entities`; unchanged files are skipped on re-run
- Each inserted row is tracked in `joka_entity_rows` with table, PK, and insertion order
- Duplicate `_id` handles within a single file are rejected with an error

**Sync of modified files** (`joka entity sync`):
- New files (not yet tracked) have their entity graph inserted.
- Files whose content changed since the last sync (`[modified]` per `entity status`) are reconciled **in place**: the file's entity graph is flattened depth-first (the same order rows were inserted and recorded in `joka_entity_rows`) and each entity is `UPDATE`d against the tracked row at the same position, by primary key. This is non-destructive — existing PKs are preserved, so external rows that reference them by id stay valid (no delete, so no FK conflict). Entities without an `_id` are handled fine; matching is positional, not by `_id`.
- Unchanged files (stored hash matches) are skipped. A tracked file with an empty stored hash (synced before content hashing existed) is treated as modified, and the update backfills the hash.
- The update path rewrites **all** columns of each matched row (the PK column itself is never written), so non-deterministic expressions like `{{ argon2id|… }}` produce a fresh value on every sync of a modified file.
- Sync refuses to update a modified file and recommends `entity reimport` (returning `ErrStructuralChange`) when it detects a structural change: a different number of entities than tracked (one was added or removed), an entity whose table no longer matches the tracked row at that position, or an `_id` that disagrees with the tracked row at that position (reorder/rename). Use `entity reimport` (full re-insert) or, for additive-only changes where every entity has an `_id`, `entity update`.

**Preview / dry-run** (`joka entity sync --dry-run`):
- Prints the plan without applying anything or acquiring the advisory lock: new files show the rows/columns that would be inserted; modified files show a per-column before/after diff (the "before" is read live from the DB).
- The same plan is printed before the normal confirmation prompt, so an interactive sync always shows exactly what will change before you confirm.
- Non-deterministic template columns (`{{ argon2id|… }}`, `{{ now }}`) are shown as `(regenerated)` rather than a misleading hash-vs-hash diff; insert values that depend on a not-yet-assigned PK show `(ref <handle>)`.
- `--output json` includes a `plan` object (and `dry_run: true` for `--dry-run`).
- Value comparison normalizes driver types to strings; a column stored as a SQL decimal may show a spurious diff against a YAML float that formats differently (e.g. `3.50` vs `3.5`).

**Entity status** (`joka entity status`):
- Compares entity files on disk with the tracking table
- Reports status per file: `synced` (hash matches), `modified` (hash differs), `new` (not yet synced), `orphaned` (tracked but file deleted)
- Uses SHA-256 content hashing stored in `joka_entities.content_hash`

**Entity reimport** (`joka entity reimport <file>`):
- Deletes previously inserted rows in reverse insertion order (children first, then parents)
- Re-inserts the entity graph from the YAML file
- Aborts on FK constraint violations from external references
- Updates the content hash and row tracking after successful reimport
- Requires prior sync; use `entity sync` first for new files

**Entity update** (`joka entity update <file>`):
- Additive-only alternative to reimport — never deletes existing rows
- Skips entities whose `_id` is already tracked; inserts only new ones
- Pre-populates the reference map from tracked data so new children can reference existing parents via `{{ parent.id }}`
- All entities must have `_id` (required to determine skip vs insert)
- Requires prior sync; use `entity sync` first for new files
- New rows are tracked with `insertion_order` continuing from existing maximum
