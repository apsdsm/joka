# Joka

Joka is a database migration and data management tool for MySQL and PostgreSQL. It tracks and applies SQL migrations, captures schema snapshots, syncs seed data from files to database tables, and seeds entity graphs with parent-child relationships.

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

Joka needs a database connection string. Either add a `.env` file in the directory you run from, or set `DATABASE_URL` as an environment variable. You can point to a specific env file with `--env`.

**MySQL:**
```
DATABASE_URL=user:pass@tcp(localhost:3306)/my_db
```

**PostgreSQL:**
```
DATABASE_URL=postgresql://user:pass@localhost:5432/my_db?sslmode=disable
```

The driver is auto-detected from the URL format. PostgreSQL URLs start with `postgres://` or `postgresql://`; everything else is treated as MySQL.

### Configuration File

Create a `.jokarc.yaml` in your project root to configure paths and table sync settings:

```yaml
migrations: devops/migrations
templates: devops/templates
entities: devops/entities
tables:
  - name: email_templates
    strategy: truncate
  - name: settings
    strategy: truncate
```

All fields are optional. CLI flags override `.jokarc.yaml` values. If neither is provided, defaults apply (`devops/migrations`, `devops/templates`, `devops/entities`).

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
├── email_templates/
│   ├── welcome.yaml
│   └── reminder.yaml
└── settings/
    └── defaults.csv
```

YAML files represent single rows, CSV files represent multiple rows. Tables and their sync strategies are configured in `.jokarc.yaml`.

### Entity Files

Entity files define seed data with parent-child relationships. They live in the entities directory (defaults to `devops/entities/`):

```
devops/entities/
├── admin_user.yaml
└── test_data.yaml
```

Each file contains an `entities` list. Entities use reserved keys (prefixed with `_`) for metadata:

| Key | Required | Description |
|-----|----------|-------------|
| `_is` | Yes | Target table name |
| `_id` | No | Reference handle for linking parent/child rows |
| `_pk` | No | Primary key column name (defaults to `id`) |
| `_has` | No | List of child entities |

All other keys are treated as column-value pairs.

**Basic entity:**

```yaml
entities:
  - _is: users
    _id: admin
    name: Admin
    email: admin@example.com
```

**Parent-child relationships:**

```yaml
entities:
  - _is: users
    _id: alice
    name: Alice
    _has:
      - _is: profiles
        user_id: "{{ alice.id }}"
        bio: "Hello world"
```

When a parent is inserted, its auto-generated primary key is stored under its `_id` handle. Children can reference it with `{{ <handle>.id }}`.

**Custom primary key:**

```yaml
entities:
  - _is: legacy_accounts
    _pk: account_id
    _id: main_account
    name: Main Account
```

**Template expressions:**

String values wrapped in `{{ }}` are resolved at insert time:

| Expression | Result |
|------------|--------|
| `{{ now }}` | Current UTC timestamp (`2006-01-02 15:04:05`) |
| `{{ <ref>.id }}` | Auto-generated primary key of a previously inserted entity |
| `{{ argon2id\|password }}` | Argon2id hash of the given plaintext |
| `{{ lookup\|table,return_col,where_col=value }}` | Query a value from an existing table row |

The `lookup` expression is useful for referencing rows seeded outside the entity file (via templates or migrations), e.g. `{{ lookup|industry_types,id,code=RESTAURANT }}`.

Entity files are tracked in a `joka_entities` table. Individual inserted rows are tracked in `joka_entity_rows` for reimport and update support. Files that have already been synced are skipped on subsequent runs.

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

Syncs template/seed data from files to database tables based on the `tables` config in `.jokarc.yaml`. Currently supports the `truncate` strategy (delete all rows, then insert from files). Runs in a transaction with advisory locking.

### `joka entity sync`

Syncs entity YAML files to the database. Inserts entity graphs depth-first (parents before children), resolving template expressions along the way. Runs in a transaction with advisory locking. Already-synced files are skipped.

### `joka entity status`

Shows the sync status of each entity file: `synced` (hash matches), `modified` (file changed since last sync), `new` (not yet synced), or `orphaned` (tracked but file deleted). Uses SHA-256 content hashing.

### `joka entity reimport <file>`

Deletes previously inserted rows in reverse insertion order (children first, then parents) and re-inserts the entity graph from the YAML file. Aborts on FK constraint violations from external references. Requires prior sync — use `entity sync` first for new files.

### `joka entity update <file>`

Adds new entities from a file without deleting existing rows. Entities whose `_id` is already tracked are skipped; only new ones are inserted. Existing parent PKs are loaded into the reference map so new children can reference them via `{{ parent.id }}`.

All entities must have `_id` in update mode (required to determine skip vs insert). Requires prior sync — use `entity sync` first for new files.

```bash
# 1. Initial sync
joka entity sync

# 2. Add a new child entity to admin_user.yaml
# 3. Run update — existing entities are kept, new ones are inserted
joka entity update admin_user.yaml
```

### `joka unlock`

Force-releases an advisory lock left behind by a crashed process. Shows who held the lock before releasing it.

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--env` | `-e` | `.env` | Path to the environment file |
| `--migrations` | `-m` | `devops/migrations` | Path to the migrations directory |
| `--templates` | `-t` | `devops/templates` | Path to the templates directory |
| `--entities` | | `devops/entities` | Path to the entities directory |
| `--auto` | `-a` | `false` | Skip confirmation prompts |
| `--output` | `-o` | `text` | Output format: `text` or `json` |
| `--ignore-foreign-keys` | | `false` | Disable FK checks during data sync truncate (MySQL) |

## How It Works

Joka uses four internal tables (all prefixed with `joka_`):

- **`joka_migrations`** — Tracks which migrations have been applied and when.
- **`joka_lock`** — Advisory lock table (at most one row). Prevents concurrent `migrate up`, `data sync`, or `entity sync` runs.
- **`joka_snapshots`** — Stores a full schema snapshot (JSON of all `CREATE TABLE` statements) after each migration is applied.
- **`joka_entities`** — Tracks which entity files have been synced (with content hashes for change detection).
- **`joka_entity_rows`** — Tracks individual rows inserted per entity file, enabling reimport (delete + re-insert) and update (additive insert).

The lock, snapshot, entity, and entity row tables are created automatically on first use. Only `joka_migrations` requires `joka init`.
