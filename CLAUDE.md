# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Joka is a MySQL migration management tool written in Python. It tracks and applies SQL migrations in order using a `migrations` table in the database.

## Development Commands

```bash
# Run commands during development
uv run python joka/__main__.py [command] [options]

# Example: check migration status
uv run python joka/__main__.py migrate status

# Example: create a new migration
uv run python joka/__main__.py make "add_users_table"

# Example: apply pending migrations
uv run python joka/__main__.py migrate up

# Run tests (requires Docker running for testcontainers)
uv run pytest tests/ -v

# Run a single test
uv run pytest tests/test_db.py::TestMigrationsTable::test_create_migrations_table -v
```

## Architecture

The codebase follows a layered architecture:

- **`__main__.py`** - CLI entry point using Typer. Creates `AppState` with config and passes to command handlers.
- **`commands/`** - Command implementations (init, migrate/up, make, data/sync)
- **`services/`** - Business logic (migration chain validation, templates management)
- **`infra/`** - Infrastructure layer (async SQLAlchemy database operations, filesystem operations, CLI utilities)
- **`db/`** - Database row models (`MigrationRow`) - pure representations of table rows
- **`files/`** - File-based models (`MigrationFile`, `TemplatesConfig`, `Table`, etc.) - representations of things loaded from disk
- **`entities/`** - Domain aggregates (`Migration`) - rich objects combining DB and file state

## Key Technical Details

- **Async-first**: All database operations use SQLAlchemy 2.0+ async with asyncmy driver
- **Configuration**: Requires `DATABASE_URL` in `.env` file or environment variable
- **Migration files**: Named `YYMMDDHHMMSS_description.sql` in `devops/migrations/` by default
- **CLI options**: `--env` for .env path, `--migrations` for migrations dir, `--templates` for templates dir, `--auto` for auto-confirm

## Database Schema

The migrations table:
```sql
CREATE TABLE migrations (
    id INT AUTO_INCREMENT PRIMARY KEY,
    migration_index VARCHAR(255) NOT NULL UNIQUE,
    applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
)
```

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
