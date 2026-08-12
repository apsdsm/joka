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

`joka migrate consolidate` additionally needs your database's own dump tool on `PATH` — `pg_dump` for PostgreSQL, `mysqldump` for MySQL. No other command requires them.

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

Instead of an env var you can declare the connection in `.jokarc.yaml` (see **Connection** below) — useful for pulling the password from a secret store without writing it to disk.

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

### Connection

By default joka reads `DATABASE_URL` from the environment (the behaviour above). You can instead declare a `connection:` block. `source` can usually be omitted — it's inferred:

| You provide | Inferred `source` |
|-------------|-------------------|
| nothing | `env` (DATABASE_URL) |
| `url:` or `password:` (or bare host/driver) | `literal` |
| a `secret:` block | `aws_secrets_manager` |

**Literal** — connection data in the file itself (plaintext; for local / non-sensitive DBs only). Either a full DSN or structured parts:

```yaml
connection:
  url: root:root@tcp(localhost:3306)/my_db   # full DSN, used verbatim
# --- or ---
connection:
  driver: mysql
  host: localhost
  port: 3306
  user: root
  database: my_db
  password: root                              # inline password; joka builds a URL-safe DSN
```

**AWS Secrets Manager** — resolve the DSN from a secret at runtime:

```yaml
connection:
  source: aws_secrets_manager   # "env" (default) | "aws_secrets_manager"
  driver: mysql                 # mysql (default) | postgres
  host: 127.0.0.1
  port: 3306
  user: root
  database: my_db
  secret:
    secret_id: my-app/db        # Secrets Manager secret id
    region: ap-northeast-1      # optional; falls back to AWS_REGION / default chain
    password_key: db_password   # JSON key in the secret holding the password
```

Two modes for `aws_secrets_manager`:

- **Assembly** (above): joka builds a URL-safe DSN from `driver/host/port/user/database/params`, taking the password from `secret.password_key`. Use when the secret holds only the password.
- **Whole-URL**: set `secret.url_key` to a JSON key holding a complete DSN (or store the secret as a plain string and omit both keys). joka uses that value verbatim.

AWS credentials come from the default chain (env vars, shared config, SSO, instance role). `source: env` requires no AWS setup.

### Secret sources for entity templates

Entity files can pull secret values from AWS Secrets Manager instead of embedding them as literals (see **Template expressions** below). Declare named sources in a top-level `secrets:` map — or per profile, where the profile's entries override same-named base sources and the rest are inherited:

```yaml
secrets:
  seed:                            # referenced in templates as asm.seed.<key>
    secret_id: my-app/seed/dev1    # Secrets Manager secret id (a JSON object)
    region: ap-northeast-1
```

A template reference `{{ asm.seed.api_key }}` resolves to the value of JSON key `api_key` in that secret. Each source is fetched once per command and cached; resolved values are never printed (plans and dry-runs show them as generated).

### Profiles

Define `profiles:` to keep multiple environments in one `.jokarc.yaml`, selected with `--profile`/`-p`. A profile overlays the base config — any field it sets wins, the rest is inherited:

```yaml
migrations: devops/migrations
entities: devops/entities

profiles:
  local:
    connection: { source: env }            # uses DATABASE_URL
  dev-remote:
    connection:
      source: aws_secrets_manager
      driver: mysql
      host: 127.0.0.1
      port: 3307
      user: root
      database: my_db
      secret: { secret_id: my-app/db, region: ap-northeast-1, password_key: db_password }
```

```bash
joka --profile dev-remote migrate up --auto
```

With no `--profile`, the base config is used (so existing configs keep working unchanged).

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
| `{{ sha256\|value }}` | SHA-256 hex digest of the given value |
| `{{ lookup\|table,return_col,where_col=value }}` | Query a value from an existing table row |
| `{{ asm.<source>.<key> }}` | Value of JSON key `<key>` in the secret configured under `<source>` (see **Secret sources**) |

The `lookup` expression is useful for referencing rows seeded outside the entity file (via templates or migrations), e.g. `{{ lookup|industry_types,id,code=RESTAURANT }}`.

An `argon2id`/`sha256` argument starting with `asm.` is resolved as a secret reference before hashing — so `{{ sha256|asm.seed.admin_key }}` hashes the secret's value, and the real secret never lives in the YAML. Any other argument is a literal, exactly as before. `<source>` and `<key>` are dot-free identifiers; the secret id itself (which may contain `/`) lives in `.jokarc.yaml`. Resolved secret values never appear in output — plans and diffs display these columns as `(generated)`/`(regenerated)`.

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

### `joka migrate consolidate --up-to <migration_index>`

Squashes the applied migration history into a single baseline file, dumped by the database's own tool.

Joka does not write the schema itself. It shells out to **`pg_dump`** or **`mysqldump`** — they are the reference implementations, and anything joka reconstructed by hand would silently lose whatever it did not know about. Joka's job here is bookkeeping: write the baseline, remove the tracking rows it replaces, delete the files it supersedes.

**Requires `pg_dump` (PostgreSQL) or `mysqldump` (MySQL) on `PATH`.** `pg_dump` also refuses to run against a server newer than itself, so keep the client version at or above the server's.

```
$ joka migrate status
Migration 250115093000 - Status: applied
Migration 250116140000 - Status: applied
Migration 250201100000 - Status: applied

$ joka migrate consolidate --up-to 250201100000
```

All three files are replaced by `250201100000_consolidated.sql`.

**`--up-to` must name the last applied migration.** A dump describes the schema as it is *now*, which reflects every applied migration — so consolidating "up to" an earlier index would write a baseline that does not match the index it carries. Pending (unapplied) files after the target are left alone. This is the one thing the command cannot do that the old snapshot-based version claimed to: keep recent migrations un-squashed.

**Bookkeeping.** The baseline keeps the target's index, so the records it replaced are removed from `joka_migrations` (and their `joka_snapshots` rows with them) in a single transaction. The chain is matched positionally, so leaving stale rows behind would break every subsequent joka command on that database. Other databases that applied the original migrations still need their own `joka_migrations` reconciled before they can migrate against the consolidated directory.

**What the baseline contains, per driver:**

- **PostgreSQL** — everything `pg_dump --schema-only` emits: tables, indexes, constraints, sequences, identity and generated columns, types, domains, views, materialized views, functions, triggers, extensions. Its `\restrict` / `\unrestrict` psql directives are stripped, since they are client commands rather than SQL. Foreign keys arrive as trailing `ALTER TABLE` statements, so table order does not matter.
- **MySQL** — table structures only. `mysqldump` wraps views, routines and triggers in constructs joka's applier cannot run (`DELIMITER`, which is a mysql-client directive, and multi-line `/*!NNNNN ... */` conditional blocks), so consolidation **refuses** when the schema contains any of them and lists what it found. Pass `--allow-unsupported` if they are managed outside joka's migrations and you accept their absence from the baseline. mysqldump's single-line conditional statements are replaced with plain `SET FOREIGN_KEY_CHECKS` toggles, because joka's SQL splitter treats conditional comments as comments and would drop them.

**Checks before anything is deleted.** The dump runs first; a missing binary, a version mismatch, or a dump that does not contain one `CREATE TABLE` per table in the database all abort before any file is written or removed.

### `joka data sync`

Syncs template/seed data from files to database tables based on the `tables` config in `.jokarc.yaml`. Currently supports the `truncate` strategy (delete all rows, then insert from files). Runs in a transaction with advisory locking.

### `joka entity sync`

Syncs entity YAML files to the database. New files have their entity graph inserted depth-first (parents before children), resolving template expressions along the way. Files that changed since the last sync (`[modified]`) are reconciled **in place**: each entity is updated by primary key against the tracked row at the same depth-first position — existing PKs are preserved (no delete, so no FK conflict) and entities without an `_id` are handled fine. Unchanged files are skipped. Runs in a transaction with advisory locking.

If a modified file changed structurally (a different number of entities than tracked, an entity's table changed, or an `_id` that disagrees with the tracked row at that position), sync refuses to guess and recommends `entity reimport` instead.

Before applying, sync prints a plan — new files show the rows to be inserted, and modified files show a per-column before/after diff. Use `--dry-run` to print the plan and exit without changing anything (and without taking the advisory lock). Non-deterministic columns like `{{ argon2id|… }}` and `{{ now }}` are shown as `(regenerated)`; secret-backed columns (`{{ asm.… }}`, hashed or plain) are also redacted this way and are never fetched or displayed at plan time. A `{{ lookup|… }}` whose target row doesn't exist yet (e.g. it's inserted by another file in the same sync) is shown as `(lookup, resolved at apply time)` rather than failing the plan. With `--output json`, the plan is included as a `plan` object.

```bash
# see exactly what a sync would change, without applying
joka entity sync --dry-run
```

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
| `--profile` | `-p` | | Config profile to use (from `.jokarc.yaml` `profiles:`) |
| `--migrations` | `-m` | `devops/migrations` | Path to the migrations directory |
| `--templates` | `-t` | `devops/templates` | Path to the templates directory |
| `--entities` | | `devops/entities` | Path to the entities directory |
| `--auto` | `-a` | `false` | Skip confirmation prompts |
| `--output` | `-o` | `text` | Output format: `text` or `json` |
| `--up-to` | | | Migration index to consolidate up to (required for `migrate consolidate`) |
| `--allow-unsupported` | | `false` | Consolidate even though the dump will not carry every object (MySQL views/routines/triggers) |
| `--ignore-foreign-keys` | | `false` | Disable FK checks during data sync truncate (MySQL) |

## How It Works

Joka uses four internal tables (all prefixed with `joka_`):

- **`joka_migrations`** — Tracks which migrations have been applied and when.
- **`joka_lock`** — Advisory lock table (at most one row). Prevents concurrent `migrate up`, `data sync`, or `entity sync` runs.
- **`joka_snapshots`** — Stores a full schema snapshot (JSON of all `CREATE TABLE` statements) after each migration is applied.
- **`joka_entities`** — Tracks which entity files have been synced (with content hashes for change detection).
- **`joka_entity_rows`** — Tracks individual rows inserted per entity file, enabling reimport (delete + re-insert) and update (additive insert).

The lock, snapshot, entity, and entity row tables are created automatically on first use. Only `joka_migrations` requires `joka init`.
