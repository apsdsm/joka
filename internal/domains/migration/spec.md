# Migration

Tracks and applies SQL migration files against a MySQL database. Migrations are ordered by a timestamp index and applied within transactions. A `migrations` table in the database records which migrations have been applied.

## domain

- `Migration` — aggregate combining database state (applied row) and file state (SQL file on disk) for a single migration. Has a computed `Status` field: `applied`, `pending`, `out_of_order`, or `file_missing`.
- Sentinel errors: `ErrNoMigrationTable`, `ErrMigrationAlreadyExists`, `ErrMigrationTableCreation`.

## app

- `GetMigrationChainAction` — reads migration files from disk and applied migrations from the database, then merges them into an ordered `[]Migration` with computed statuses. Detects chain breaks (mismatched indexes, missing files).
- `ApplyAction` — executes a single migration's SQL file and records it as applied, both within a provided transaction.

## infra

### data model

`migrations` table:

| column          | type         | notes                              |
|-----------------|--------------|------------------------------------|
| id              | INT AUTO_INCREMENT | primary key                  |
| migration_index | VARCHAR(255) | unique, matches filename timestamp |
| applied_at      | TIMESTAMP    | defaults to current timestamp      |

Infra models: `MigrationRow` (maps to a row in the table), `MigrationFile` (parsed from a filename on disk).

### file structure

Migration files live in a configurable directory (default `devops/migrations/`). Each file follows the naming convention `YYMMDDHHMMSS_description.sql`. Files are sorted by the timestamp prefix to determine application order.
