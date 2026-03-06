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
go run . data sync
go run . entity sync
go run . entity status
go run . entity reimport admin_user.yaml
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

## Key Technical Details

- **Go 1.25+** with `github.com/go-sql-driver/mysql` and `github.com/lib/pq`
- **Driver auto-detection**: The database driver is detected from the `DATABASE_URL` format. PostgreSQL DSNs start with `postgres://` or `postgresql://`; everything else is assumed MySQL.
- **Multi-statement SQL**: MySQL DSN is configured with `multiStatements=true`; PostgreSQL handles multiple statements natively.
- **Configuration**: Requires `DATABASE_URL` in `.env` file or environment variable.
  - MySQL: `user:pass@tcp(host:port)/dbname`
  - PostgreSQL: `postgresql://user:pass@host:port/dbname?sslmode=disable`
- **Migration files**: Named `YYMMDDHHMMSS_description.sql` in `devops/migrations/` by default.
- **CLI flags**: `--env` for .env path, `--migrations` for migrations dir, `--templates` for templates dir, `--entities` for entities dir, `--auto` for auto-confirm.
- **Advisory locking**: `migrate up`, `data sync`, `entity sync`, and `entity reimport` acquire a DB lock before running. Use `joka unlock` if a process crashes without releasing.

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
- `{{ lookup|table,return_col,where_col=value }}` — Query a value from an existing table row (e.g. `{{ lookup|industry_types,id,code=RESTAURANT }}`). Useful for referencing rows seeded outside the entity file (via templates or migrations)

**Insertion behavior**:
- Entities are inserted depth-first: parent first, then children in order
- Each entity's auto-generated PK is stored in a reference map under its `_id` handle
- Children can reference any previously inserted entity via `{{ handle.id }}`
- All inserts within a file run in a single transaction
- Files are tracked in `joka_entities`; already-synced files are skipped on re-run
- Each inserted row is tracked in `joka_entity_rows` with table, PK, and insertion order
- Duplicate `_id` handles within a single file are rejected with an error

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
