# Migration Domain

Owns the full lifecycle of SQL schema migrations: creating migration files, tracking which have been applied, applying pending ones, and capturing schema snapshots after each apply.

## Tables

### `joka_migrations`

Tracks which migrations have been applied. Each row is one migration.

| Column | Type | Notes |
|--------|------|-------|
| `id` | `INT AUTO_INCREMENT PK` | Insertion order, used to maintain the chain |
| `migration_index` | `VARCHAR(255) UNIQUE` | 12-digit timestamp from the filename (e.g. `240615143022`) |
| `applied_at` | `TIMESTAMP DEFAULT CURRENT_TIMESTAMP` | When the migration was applied |

### `joka_snapshots`

Stores a full schema snapshot after each migration is applied. Auto-created on first use.

| Column | Type | Notes |
|--------|------|-------|
| `id` | `INT AUTO_INCREMENT PK` | Insertion order |
| `migration_index` | `VARCHAR(255) UNIQUE` | Links to the migration that produced this snapshot |
| `schema_snapshot` | `LONGTEXT` | JSON object: `{"table_name": "CREATE TABLE ..."}` for all non-joka tables |
| `captured_at` | `TIMESTAMP DEFAULT CURRENT_TIMESTAMP` | When the snapshot was taken |

## Migration Files

Files live in the migrations directory (`devops/migrations/` by default) and follow the naming convention:

```
YYMMDDHHMMSS_description.sql
```

- 12-digit timestamp prefix (year, month, day, hour, minute, second)
- Underscore separator
- Descriptive name (snake_case)
- `.sql` extension

Example: `240615143022_create_users_table.sql`

Files that don't match this pattern are silently ignored.

## Core Concepts

### Migration Chain

The chain is the ordered merge of migration files on disk and applied rows in the database. Each migration gets a computed status:

- **applied** — File exists and a matching row exists in `joka_migrations` at the same position.
- **pending** — File exists but no corresponding applied row. Ready to be applied.
- **out_of_order** — Reserved for future use. Would indicate a file inserted before an already-applied migration.
- **file_missing** — An applied row exists but the corresponding file is missing from disk.

The chain is validated by walking both lists (files sorted by index, rows sorted by id) in lockstep. If a file's index doesn't match the applied row's `migration_index` at the same position, the chain is considered broken and the operation fails.

### Apply Flow

When `migrate up` runs, each pending migration goes through three steps:

1. **Execute SQL** — Read the `.sql` file and run it against the database. Multi-statement files are supported (the DSN has `multiStatements=true`).
2. **Record** — Insert a row into `joka_migrations` with the migration's index.
3. **Snapshot** — Query `SHOW CREATE TABLE` for every non-joka user table and store the result as JSON in `joka_snapshots`.

All pending migrations are applied inside a single database transaction. If any step fails, the entire batch is rolled back.

## Layer Responsibilities

### `domain/`
Pure data types and error sentinels. No dependencies on infrastructure.

- `Migration` — The aggregate combining file state, DB state, and computed status.
- `ErrNoMigrationTable`, `ErrMigrationAlreadyExists`, `ErrMigrationTableCreation` — Domain error types.

### `app/`
Use-case actions. Depend on the `DBAdapter` interface, not on MySQL directly.

- `CreateMigrationTableAction` — Creates the `joka_migrations` table (idempotent-ish: returns error if exists).
- `GetMigrationChainAction` — Reads files + applied rows, merges into chain, validates integrity.
- `ApplyAction` — Runs the three-step apply flow for a single migration.
- `DBAdapter` — Interface defining all database operations the app layer needs.

### `infra/`
Infrastructure implementations.

- `MySQLDBAdapter` — Implements `DBAdapter` for MySQL. Can wrap either a raw `*sql.DB` or a `*sql.Tx`.
- `ListMigrationFiles()` — Scans a directory for migration files matching the naming pattern.
- `CreateMigrationFile()` — Creates a new empty `.sql` file with a timestamped name.
- `models/` — Flat data structs for rows (`MigrationRow`) and files (`MigrationFile`).

## Commands

| Command | What it does |
|---------|-------------|
| `joka init` | Creates the `joka_migrations` table |
| `joka make <name>` | Creates a new timestamped `.sql` file in the migrations directory |
| `joka migrate up` | Applies all pending migrations (with locking) |
| `joka migrate status` | Prints the status of every migration in the chain |
| `joka migrate snapshot [index]` | Prints the stored schema snapshot for a migration (defaults to latest) |
