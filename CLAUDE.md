# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Joka is a MySQL migration and data management tool written in Go. It tracks and applies SQL migrations using a `joka_migrations` table, captures schema snapshots after each migration, and syncs seed data from files to database tables.

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
- **`db/`** — Database utilities (`Open`, `TableExists`, `EnsureMultiStatements`).
- **`cmd/`** — Command handlers. Each receives dependencies and calls into domain actions.
- **`internal/domains/`** — Domain logic, organized by bounded context.

### Domains

- **`migration/`** — Migration lifecycle: create files, track applied migrations, apply pending ones, capture schema snapshots.
- **`lock/`** — DB-backed advisory locking via `joka_lock` table. Prevents concurrent mutating operations.
- **`template/`** — Syncs seed/reference data from YAML/CSV files to database tables.

### Layer pattern (within each domain)

- **`domain/`** — Pure types, constants, and error sentinels. No infrastructure dependencies.
- **`app/`** — Use-case actions and interfaces (e.g. `DBAdapter`). Depends on domain types, not on MySQL.
- **`infra/`** — MySQL and filesystem implementations. Implements the interfaces defined in `app/`.
- **`infra/models/`** — Flat structs for DB rows and file representations.

## Key Technical Details

- **Go 1.25+** with `github.com/go-sql-driver/mysql`
- **Multi-statement SQL**: DSN is configured with `multiStatements=true` so migration files can contain multiple statements separated by semicolons.
- **Configuration**: Requires `DATABASE_URL` in `.env` file or environment variable (MySQL DSN format: `user:pass@tcp(host:port)/dbname`).
- **Migration files**: Named `YYMMDDHHMMSS_description.sql` in `devops/migrations/` by default.
- **CLI flags**: `--env` for .env path, `--migrations` for migrations dir, `--templates` for templates dir, `--auto` for auto-confirm.
- **Advisory locking**: `migrate up` and `data sync` acquire a DB lock before running. Use `joka unlock` if a process crashes without releasing.

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

`joka_lock` and `joka_snapshots` are auto-created on first use. Only `joka_migrations` requires `joka init`.

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
