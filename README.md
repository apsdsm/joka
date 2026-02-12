# Joka

Joka is a MySQL migration and data management tool. It tracks and applies SQL migrations, captures schema snapshots, and syncs seed data from files to database tables.

<p align="center">
  <img src="joka.jpg" alt="joka" width="400">
</p>

## Install

Build from source (requires Go 1.25+):

```bash
go install github.com/apsdsm/joka@latest
```

## Setup

### Database URL

Joka needs a MySQL connection string. Either add a `.env` file in the directory you run from, or set `DATABASE_URL` as an environment variable. You can point to a specific env file with `--env`.

```
DATABASE_URL=user:pass@tcp(localhost:3306)/my_db
```

### Migration Files

Put your migrations in a single directory (defaults to `devops/migrations/`). Files must follow the naming pattern `YYMMDDHHMMSS_description.sql`:

```
devops/migrations/
├── 250115093000_create_users.sql
├── 250116140000_add_email_index.sql
└── 250201100000_create_orders.sql
```

Files are applied in order of their timestamp prefix. Each file can contain multiple SQL statements.

### Template Files

Seed/reference data lives in the templates directory (defaults to `devops/templates/`):

```
devops/templates/
├── _config.yaml
├── email_templates/
│   ├── welcome.yaml
│   └── reminder.yaml
└── settings/
    └── defaults.csv
```

YAML files represent single rows, CSV files represent multiple rows. See `_config.yaml` for table configuration and sync strategies.

## Commands

### `joka init`

Creates the `joka_migrations` tracking table. Run this once before your first migration.

### `joka make <name>`

Creates a new timestamped migration file in the migrations directory.

```bash
joka make create_users_table
# Creates: devops/migrations/250615143022_create_users_table.sql
```

### `joka migrate up`

Shows current migration status, then applies any pending migrations (with confirmation). All pending migrations run in a single transaction — if one fails, they all roll back. An advisory lock prevents concurrent runs.

### `joka migrate status`

Shows the status of every migration (applied or pending) without applying anything.

### `joka migrate snapshot [migration_index]`

Displays the schema snapshot captured after a migration was applied. Shows `CREATE TABLE` statements for all user tables. Omit the index to see the latest snapshot.

### `joka data sync`

Syncs template/seed data from files to database tables based on `_config.yaml`. Currently supports the `truncate` strategy (delete all rows, then insert from files). Runs in a transaction with advisory locking.

### `joka unlock`

Force-releases an advisory lock left behind by a crashed process. Shows who held the lock before releasing it.

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--env` | `-e` | `.env` | Path to the environment file |
| `--migrations` | `-m` | `devops/migrations` | Path to the migrations directory |
| `--templates` | `-t` | `devops/templates` | Path to the templates directory |
| `--auto` | `-a` | `false` | Skip confirmation prompts |

## How It Works

Joka uses three internal tables (all prefixed with `joka_`):

- **`joka_migrations`** — Tracks which migrations have been applied and when.
- **`joka_lock`** — Advisory lock table (at most one row). Prevents concurrent `migrate up` or `data sync` runs.
- **`joka_snapshots`** — Stores a full schema snapshot (JSON of all `CREATE TABLE` statements) after each migration is applied.

The lock and snapshot tables are created automatically on first use. Only `joka_migrations` requires `joka init`.

