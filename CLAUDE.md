# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Joka is a database migration and data management tool written in Go. It supports **PostgreSQL**. It tracks and applies SQL migrations using a `joka_migrations` table, captures schema snapshots after each migration, and syncs seed data from files to database tables.

MySQL was supported until v0.14.0. It was removed because no project using joka ran MySQL, the two drivers had diverged (consolidation was PostgreSQL-only, snapshots were reconstructed differently per driver), and keeping both honest cost more than it returned. `db.Open` rejects a non-PostgreSQL DSN with `db.ErrUnsupportedDriver` so an old config gets a sentence rather than a driver error.

Module path: `github.com/apsdsm/joka`

## Development Commands

```bash
# Build
go build ./...

# Run commands during development
go run . [command] [options]

# Examples
go run . init
go run . status
go run . status --compact
go run . make "add_users_table"
go run . migrate up
go run . migrate status
go run . migrate snapshot
go run . migrate verify
go run . migrate consolidate --up-to 250116140000
go run . data sync
go run . entity sync
go run . entity status
go run . entity diff admin_user.yaml
go run . entity reimport admin_user.yaml
go run . entity forget admin_user.yaml
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
- **`internal/status/`** — Cross-domain read model behind `joka status`. Sits above the domains (they never import each other) and reads from all of them.
- **`internal/textui/`** — Rune-aware terminal table used by `joka status` and `joka entity diff`. Widths are counted in runes, never bytes: `✓` is three bytes and `·` is two, so byte padding misaligns them by different amounts.

### Domains

- **`migration/`** — Migration lifecycle: create files, track applied migrations, apply pending ones, capture schema snapshots.
- **`lock/`** — DB-backed advisory locking via `joka_lock` table. Prevents concurrent mutating operations.
- **`template/`** — Syncs seed/reference data from YAML/CSV files to database tables.
- **`entity/`** — Syncs entity graphs (parent-child seed data) from YAML files with reference resolution.

### Layer pattern (within each domain)

- **`domain/`** — Pure types, constants, and error sentinels. No infrastructure dependencies.
- **`app/`** — Use-case actions and interfaces (e.g. `DBAdapter`). Depends on domain types, not on specific databases.
- **`infra/`** — PostgreSQL and filesystem implementations. Implements the interfaces defined in `app/`. The database adapter lives in `postgres.go`; helpers shared within the package (the `DBTX` interface, small conversions) live in `shared.go`.
- **`infra/models/`** — Flat structs for DB rows and file representations.

## Versioning

The version is defined as a `const` in `main.go`. When bumping the version:
1. Update the `version` constant in `main.go`
2. Create a git tag matching the version (e.g. `git tag v0.3.0`)
3. Push the tag (e.g. `git push origin v0.3.0`)

## Key Technical Details

- **Go 1.25+** with `github.com/lib/pq`
- **External binaries**: `migrate consolidate` requires `pg_dump` on `PATH` (PostgreSQL only). No other command shells out. Tests that need it skip when absent (`exec.LookPath`).
- **PostgreSQL only**: `db.IsPostgresDSN` requires the URL to start with `postgres://` or `postgresql://`; `db.Open` refuses anything else with `db.ErrUnsupportedDriver`. `connection.assembleDSN` refuses a `driver:` other than `postgres`/`postgresql`/empty the same way, so a stale `.jokarc.yaml` fails with an explanation instead of a timeout.
- **Multi-statement SQL**: PostgreSQL handles multiple statements natively. `db.SplitSQLStatements` still splits migration files so each statement can be applied and reported individually.
- **Connection**: by default the DSN comes from `DATABASE_URL` (`.env` or environment). The
  `.jokarc.yaml` may instead declare a `connection:` block (`internal/connection`) whose `source`
  is `env`, `literal` (inline `url:`/`password:`), or `aws_secrets_manager` (assemble from parts +
  a secret key, or a whole-URL key). See README for the schema. A top-level (or per-profile)
  `secrets:` map declares named Secrets Manager sources for entity template `asm.` references;
  profile entries override same-named base sources.
  - PostgreSQL: `postgresql://user:pass@host:port/dbname?sslmode=disable`
- **Profiles**: `.jokarc.yaml` may define a `profiles:` map; `--profile <name>` overlays a profile
  (migrations/entities/connection) onto the base config. No `--profile` uses the base.
- **Migration files**: Named `YYMMDDHHMMSS_description.sql` in `devops/migrations/` by default.
- **CLI flags**: `--env` for .env path, `--profile`/`-p` for the config profile, `--migrations` for migrations dir, `--templates` for templates dir, `--entities` for entities dir, `--auto` for auto-confirm, `--output` / `-o` for output format (`text` or `json`).
- **JSON output**: `--output json` emits a single JSON object per command (no color, no prompts). All responses include a `"status"` field (`"ok"` or `"error"`). When `--output json` is set, confirmations are auto-skipped (like `--auto`).
- **Advisory locking**: `migrate up`, `data sync`, `entity sync`, `entity reimport`, `entity forget`, `drop`, and `reset` acquire a DB lock before running. (`reset` holds one outer lock for the whole pipeline.) Use `joka unlock` if a process crashes without releasing.

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
-- Entity tracking: one jsonb document per key, only 'entities' today
CREATE TABLE joka_state (
    key VARCHAR(64) PRIMARY KEY,
    doc JSONB NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)
```

`joka_entities` and `joka_entity_rows` held this until tracking version 3 and are dropped by the
upgrade that moves them. The backend still **reads** them, because read-only commands never upgrade
and `joka status` has to describe a database no mutating command has touched yet. Nothing creates or
writes them.

`joka_lock`, `joka_snapshots`, `joka_state`, and `joka_meta` are auto-created on first use. Only `joka_migrations` requires `joka init`. The one exception is `joka status`, which never creates them — a missing tracking table is something it reports. `entity status` and `entity diff` create nothing either: a state that has never been written reads as empty.

## Status report

`joka status` (`internal/status`, `cmd/status`) builds one read-only report of the three
states joka works between: **declared** (the devops folder), **tracked** (the `joka_*` tables) and
**live** (the database). Every mismatch joka can hit is a disagreement between two of those.

Coverage per edge, and which command reported it before:

| Edge | Previously | In the report |
|---|---|---|
| migration files vs `joka_migrations` | `migrate status` | MIGRATIONS section |
| snapshot vs live schema | `migrate verify` | drift subsection |
| entity file hash vs `joka_entities` | `entity status` | ENTITIES section |
| `joka_entity_rows` vs live rows | nothing | ENTITIES `live` / `missing_rows` |
| template records vs table row counts | nothing | TEMPLATES section |
| held advisory lock | error text on the next mutating command | LOCK section |

### Design notes

- **Read-only, including tracking tables.** Every other command auto-creates the `joka_*` table it
  needs. Status must not: a missing tracking table is a finding. Reads that would create one
  (`GetLatestSnapshotIndex` calls `EnsureSnapshotsTable`; the lock adapter's `GetLock` calls
  `EnsureTable`) are gated behind a `Probe.TableExists` check. `TestStatusIsReadOnly` asserts a bare
  database still has no `joka_*` tables after a full report.
- **Alignment is by index, not by position.** `GetMigrationChainAction` (behind `migrate status` and
  `migrate up`) walks files and rows positionally and returns an error the moment they disagree, so
  on exactly the databases worth diagnosing it reports nothing. `buildMigrations` takes the union of
  indexes from both sides, which is what makes `file_missing` and `out_of_order` reportable.
- **The file verdict comes from sync's own check.** Whether a file counts as new, modified or synced
  is `app.FileStatusFor`, the one function `entity status` and `entity sync` also call, so the three
  cannot disagree about what a sync would rewrite. The rule they share is that an empty stored hash
  (a row synced before content hashing existed) reads as modified.
- **Not every finding has a command.** `Action.Command` is empty when joka has nothing that fixes
  the finding; the text report prints `(nothing joka can run)`. Do not invent a command in the
  actions list that does not exist — add the command first (`entity forget` was added exactly this
  way, for the orphan and deleted-row findings that previously had no answer).
- **`--compact` is one line, positionally stable.** Every section appears whether or not it has a
  problem, so the line reads the same way each time (`internal/status/compact.go`). `+N` is entity
  files new or modified, `!N` is real problems, `n/a` is a section that could not be read. It is
  rejected alongside `--output json`, which is already the whole report.
- **`--output json` is the same struct.** Text and JSON render one `status.Report`; empty slices are
  normalized to `[]` (`Report.normalize`) so consumers do not need null checks. Exit code is 0
  whenever the report could be built — `migrate verify` remains the drift gate for CI.
- **Column widths are counted in runes.** `✓` is three bytes and `·` is two, so
  `internal/textui` pads by rune count and colours whole lines rather than cells (an escape
  sequence inside a padded cell breaks the alignment it was padded for).
  `TestRenderTextAlignsMultiByteGlyphs` and `TestTableAlignsMultiByteGlyphs` guard this — both assert
  on **rune** offsets, since two aligned columns sit at different byte offsets when the glyphs differ.

## Schema drift detection

`joka migrate verify` compares the live database schema against the schema snapshot stored for the most recent applied migration. It reports tables that were added in live but missing from the snapshot, tables present in the snapshot but missing from live, and tables whose CREATE statements differ.

- Useful for catching out-of-band DDL (manual `ALTER TABLE`, columns added without a migration, etc.).
- Statements are compared verbatim. (Until v0.14.0 a MySQL `AUTO_INCREMENT=<n>` counter was stripped first; there is no PostgreSQL equivalent, so the normalisation went with the driver.)
- Exit code is non-zero when drift is detected — suitable for CI gating.

## Schema snapshots

Snapshots cover **base tables only**, and are reconstructed rather than dumped:

- Rebuilt from `pg_catalog` in `reconstructCreateTable`. Column types come from `format_type()`; identity, generated and serial columns are read from `pg_attribute`. `information_schema.columns` is deliberately **not** used: it reports `ARRAY` / `USER-DEFINED` instead of real types and has no way to express identity. Each statement it emits is terminated with `;`, including the index statements appended after the table body. Sequences owned by a `serial` column are recreated by rendering the column as `serial`/`bigserial`.

Views, functions, types, triggers and standalone sequences are **not** captured. Snapshots feed `migrate snapshot` and `migrate verify` only — **not** consolidation, which dumps the schema with pg_dump precisely because a table snapshot cannot describe a whole schema.

Note: the PostgreSQL reconstruction format changed in v0.13.0. Snapshots captured by v0.12.0 or earlier will show as drift in `migrate verify` until the next migration re-captures them.

## Consolidation

`joka migrate consolidate --up-to <index>` squashes the applied history into one `<index>_consolidated.sql`, produced by shelling out to **pg_dump** (`internal/domains/migration/infra/schema_dump.go`, the `SchemaDumper` interface). Joka generates no schema DDL of its own here — the dump tool is the reference implementation, and hand-rolled reconstruction silently drops whatever it does not model.

- **`--up-to` must be the last applied migration** (`ErrNotLastApplied`). A dump reflects the schema now, so an earlier index would carry a baseline that does not match it. Pending files after the target are untouched. Partial squash is therefore not supported.
- Consolidation does not read `joka_snapshots`, so it works on databases migrated before snapshots existed.
- `pg_dump --schema-only --no-owner --no-privileges --exclude-table=joka_*`. The wildcard also excludes the sequences owned by joka tables. `\restrict` / `\unrestrict` are stripped — psql client directives, not SQL. FKs come back as trailing `ALTER TABLE`s, which is why no FK ordering logic is needed.
- The password goes to pg_dump via `PGPASSWORD`, never in argv.
- `verifyTableCoverage` asserts one `CREATE TABLE` per table in the database before the dump is accepted.
- Order of operations: dump → write file → `RemoveMigrationRecords` (one transaction) → delete old files. Bookkeeping precedes file deletion so a failure there leaves the directory intact. The target's own record is kept — the new file carries its index, and the chain is zipped positionally.

## Wipe and reseed

- **`joka drop`** — drops every table in the current database/schema, including all `joka_*` tracking tables. Confirms unless `--auto`. Uses `DROP TABLE ... CASCADE`.
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
- `_pk` (optional) — Primary key column name, defaults to `"id"`. Used for the `RETURNING` clause when inserting
- `_has` (optional) — List of child entities, inserted after the parent
- `_once` (optional) — List of column names joka seeds on insert and never writes again

```yaml
- _is: users
  _id: admin
  email: admin@example.com
  password_hash: "{{ argon2id|admin123 }}"
  _once:
    - password_hash
```

A seed row has three kinds of column: one joka owns, one joka seeds and then lets go of, and one
joka never touches because the file does not declare it. `_once` is the middle kind, and it exists
because a user resetting their password is not a difference to resolve — it is a column the
application owns from then on. Without it every sync of a modified file rewrites the password back,
which is the problem that made `entity sync` skip already-synced files in the first place.

- **The column stays where every other column is**; `_once` only annotates it. A reader sees the
  whole row in one place, and a template in a `_once` column resolves the same way as anywhere else.
- **Naming a column the entity does not declare is refused.** There is nothing to seed, so it is a
  typo, and ignoring it would leave the author believing a column was protected when it was not.
- **The baseline keeps the insert-time hash.** joka applied the column once, and that is still the
  last thing it applied to it, so `baselineAfterUpdate` carries the entry forward rather than
  dropping it.
- **It is not a difference.** `ResolveRowChanges` skips it, so the sync preview and `entity diff`
  never show a change sync would not make. `entity diff` reports the columns once per file in
  `SeededColumns`, the same way it reports `RegeneratedColumns`.

**Template expressions** (resolved at insert time):
- `{{ now }}` — Current UTC timestamp (`2006-01-02 15:04:05`)
- `{{ <ref>.id }}` — Auto-generated PK of a previously inserted entity (looked up by `_id` handle)
- `{{ argon2id|<plaintext> }}` — Argon2id hash of the given plaintext
- `{{ sha256|<value> }}` — SHA-256 hex digest of the given value
- `{{ lookup|table,return_col,where_col=value }}` — Query a value from an existing table row (e.g. `{{ lookup|industry_types,id,code=RESTAURANT }}`). Useful for referencing rows seeded outside the entity file (via templates or migrations)
- `{{ asm.<source>.<key> }}` — Value of JSON key `<key>` in the AWS Secrets Manager secret configured under source name `<source>` in the `.jokarc.yaml` `secrets:` map (`internal/secrets`). Also valid as the argument to `sha256|`/`argon2id|` (e.g. `{{ sha256|asm.seed.admin_key }}` hashes the secret's value); any hash argument not starting with `asm.` stays a literal. Each source is fetched once per command and cached. The planner treats all `asm.` expressions as non-deterministic, so plans/dry-runs show `(generated)`/`(regenerated)` and never fetch or print secret values.

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
- A `{{ lookup|… }}` whose target row doesn't exist yet shows `(lookup, resolved at apply time)` instead of failing the plan — the row may be inserted by this same sync (e.g. a person file looking up a client file's row on a fresh database; inserts apply before updates). Lookups that resolve at plan time show the concrete value; other lookup errors still fail the plan. In JSON, deferred update columns carry `"deferred": true`.
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

**Entity forget** (`joka entity forget <file>` / `--orphans`):
- Removes the `joka_entities` record and every `joka_entity_rows` entry for a file. **Never touches
  the rows they point at, and never touches the file on disk.** The inverse of `reimport`, which
  replaces the rows and keeps the tracking.
- Answers the two states nothing else resolves: tracking whose rows were deleted by hand elsewhere,
  and an orphan (file and rows both gone, tracking left behind). `joka status` names it for both.
- **Refuses when a tracked row is still in the database** (`ErrRowsStillLive`), because dropping the
  tracking for a live row leaves a row joka does not own and the next sync inserts a second copy.
  `--force` overrides; the plan still reports the live rows either way.
- `--orphans` resolves its targets through `EntityStatusAction`, the same comparison `entity status`
  reports, so the two can never disagree about which files are orphaned.
- `ForgetEntityAction` splits `Plan` (read-only, used for the preview and the refusal) from
  `Execute`. `Execute` returns the plan it acted on, and returns it alongside `ErrRowsStillLive` too,
  so the caller shows the offending rows rather than packing them into the error string.
- Deliberately does not delete or rename files. Retiring a seed is forget + `rm`, or forget +
  `mv seed.yaml seed.yaml.off` (`DiscoverEntityFiles` only picks up `.yaml` / `.yml`).

**Entity diff** (`joka entity diff <file>`):
- Lines the declared graph up against `joka_entity_rows` and against the live rows, and prints one
  row per alignment line with a `= ≠ + - ~ !` gutter. Read-only; takes no lock and creates no
  tracking tables.
- Was built because sync's structural refusal named the symptom ("48 entities but 38 are tracked")
  without saying which entities were new, and recommended a destructive reimport. The diff showed the
  shape of the change and whether an identity match would be exact, which was the input to the
  `_id`-keyed matching decision in `proposal_entity_identity_20260826.md`. That refusal is gone; the
  diff remains the only command that shows declared, tracked and live side by side.
- **Matching**: `_id` when both sides are fully keyed, otherwise positional, with the unkeyed
  entities and rows named. Sync itself matches on `_id` only and refuses a set with a missing one, so
  the positional fallback describes a set sync would not accept — a database synced before `0c4e64d`,
  or a file not yet given `_id`s. `PositionalBreak` is the 1-based declared position where walking
  both sides in step stops describing the same row. It is diagnostic only (it was where sync would
  have written to the wrong row, back when sync matched positionally) and is computed only when both
  sides are non-empty.
- `DiffLine.Moved` is deliberately **orthogonal** to `Status`: an insert earlier in the file shifts
  every row after it, and those rows may or may not also have been edited. The gutter shows `≠` over
  `~` because the two position columns already make a move visible, while a column change is only
  visible in the notes.
- **Values are compared semantically, not as strings.** `valuesEqual` canonicalises both sides when
  both parse as JSON objects/arrays (keys sorted, whitespace dropped) — PostgreSQL renders `jsonb`
  in its own key order with a space after each colon, so a raw string compare marked every JSON
  column on every row as changed. `alignForDisplay` then renders the two sides in that same
  canonical form so the reader sees what differs rather than a key-order disagreement. Only
  both-sides-JSON is canonicalised; a scalar or a value that is JSON on one side only is compared
  raw. Shared with `entity sync --dry-run`.
- **Regenerated columns are a file property, not a row difference.** A column from a
  non-deterministic template (`{{ now }}`, `{{ argon2id|… }}`, `asm.*`) is rewritten on every sync
  whatever the row holds, so it does not count as a change or promote a row to `≠`. It is collected
  once into `EntityDiff.RegeneratedColumns` and reported in the summary. Without this, one
  `created_at: "{{ now }}"` marks every row in the file changed, forever.
- Column values are compared by default (one `GetRow` per matched live row) via
  `app.ResolveRowChanges`, which `entity sync --dry-run` also uses — so the two can never disagree
  about what a column change is. `--no-values` skips it. A row that is not in the database is never
  compared; `ChangesSkipped` says why rather than leaving an empty list to be misread as "no change".
- **The diff does not report sync's one remaining refusal.** `ErrEntityTableChanged` — an `_id`
  tracked against one table and now declared on another — has no line in the diff. The
  `SyncVerdict` and `TableChanged` fields that were meant to carry it were never assigned and have
  been removed rather than left as a JSON key that could not appear. Adding it back belongs with the
  refusal semantics in `proposal_entity_convergence_20260918.md`.
- **The `_has:` nesting is redrawn as a tree.** Rows are listed in the flat depth-first order they
  are inserted and tracked in, but `DiffLine.Depth` carries each entity's nesting level so the
  renderer can put a child under its parent. `flattenDepths` must walk the graph exactly as
  `flattenEntities` does or every depth attaches to the wrong row —
  `TestDiffEntityDepth/its_depths_line_up_with_the_flattened_order_sync_uses` guards that.
  `cmd/entity.treePrefixes` derives the `├─ │ └─` connectors from the depth sequence alone (the next
  line at the same depth before any shallower line is a following sibling), so the domain carries no
  presentation. A line with no declared side — a delete — has no nesting to report and sits at depth 0.

## Tracking version

`internal/meta` records what wrote a database's bookkeeping, in a `joka_meta` key/value table:

```sql
CREATE TABLE joka_meta (
    key VARCHAR(64) PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
)
```

| Key | Meaning |
|---|---|
| `tracking_version` | `meta.TrackingVersion` as a decimal string — the shape and meaning of the `joka_*` tables |
| `joka_version` | The joka release that last wrote. Informational; nothing branches on it |

**Why it exists.** The tracking tables changed shape three times with no marker: `content_hash` on
`joka_entities`, `ref_id`/`pk_column` on `joka_entity_rows`, and the v0.13.0 snapshot format. Each
was absorbed by sniffing — checking for a column, tolerating an empty value — which works only when
joka is the *newer* of the two. The case sniffing cannot cover is an older joka reading a database a
newer joka already wrote: it has no way to know the format moved. `Check` makes that refusable.

- **Bump `TrackingVersion`** when the meaning or shape of the tracking tables changes in a way an
  older joka would read wrongly — not for additive changes it ignores harmlessly. Every bump needs a
  line in the constant's doc comment saying what moved.
- **An absent marker reads as the current version.** A database with tracking tables but no
  `joka_meta` predates the marker; it is not from the future. It gets stamped on the next mutating
  command.
- **Check runs on connect; Stamp runs only for commands that write.** `main.go` tags mutating
  commands with the `joka:mutates` annotation and the root `PersistentPreRunE` stamps on that. A
  read-only command must leave a bare database bare, which is what makes a missing tracking table
  reportable — `TestStatusIsReadOnly` and `TestReadCreatesNothing` both guard it.
- **An unparseable version is treated as too new.** joka writes a decimal string, so anything else
  came from something this build does not understand.
- `joka status` reports the marker in its header (`written by joka 0.14.0`) and in JSON under `meta`.

## Entity identity (`_id`)

joka identifies every seeded row by its `_id`. This is being moved to gradually; the steps done so
far and the ones still open are recorded here.

### The invariants

- **Every entity declares an `_id`.** An entity without one cannot be matched across an edit, so
  there is no way to say which row it became.
- **No `_id` is claimed twice.** One `_id` identifies one row.
- **Scope is the loaded set, not the filesystem.** Uniqueness is checked across the files the
  resolved `entities:` directory contains. Parallel per-environment trees (jjc2 has `local/`,
  `dev1/` and `e2e/`) deliberately share `_id`s — they are the same logical entity for different
  environments and only one set is ever loaded. Checking any wider scope would reject a correct
  arrangement. It follows that an `_id` is unique *per database*, not universally.

`app.ValidateEntitySet` checks both and returns **every** problem rather than the first: a set that
has never been validated usually has several, and fixing them one error message at a time is
miserable. `EntitySetError` renders them into one error wrapping `domain.ErrEntitySetInvalid`.

### Where it is enforced

- **`entity sync`** validates the whole set before writing anything, and refuses.
- **`joka status`** reports problems per file (`EntityFile.IdentityProblems`) and raises an action
  with no command — an `_id` has to be authored, and choosing one is a decision about what the
  entity is.
- Sync now **parses every file**, including ones the content hash says are unchanged: `_id`
  uniqueness is a property of the whole set, so an unchanged file still has to be read to know what
  it claims. The hash decides whether a file is *written*, not whether it is *read*.

### Reserved keys

`_is` (table), `_id` (identity), `_pk` (primary key column, defaults to `id`), `_has` (children),
`_once` (columns seeded on insert and owned by the database after).

`_key` was added and removed in the same session. It named a column that identifies a row in the
database independently of its primary key, for adopting a row joka did not insert. It was never
wired to anything, and shipping an inert reserved key invites someone to write it and expect a
behaviour that is not there. If adoption is wanted later, build it on the unique constraints already
in the schema — 17 of the 18 tables jjc2 seeds declare `UNIQUE (xid)` — rather than on a new key.

`_pk` is a candidate for the same treatment: it is used 0 times across jjc2's 294 entities, every
seeded table has `PRIMARY KEY (id)`, and joka could read the column from `pg_index` instead of being
told. Not done.

### Still open

1. ~~Validation: `_id` mandatory and unique.~~ Done.
2. ~~Re-key `joka_entity_rows` on `ref_id`.~~ Done — see **Tracking upgrades** below.
3. ~~Identity matching in sync.~~ Done — see **Identity matching** below.
4. `entity diff --undeclared` and `entity forget --undeclared`, to act in bulk on the entities sync
   already reports as declared nowhere.
5. Infer the primary key column and retire `_pk`.

## Tracking upgrades

`internal/upgrade` moves a database's `joka_*` bookkeeping to `meta.TrackingVersion`. One place
records what each bump does, because the alternative — format changes absorbed by sniffing,
scattered across whichever adapter noticed — is how joka got three undocumented format changes with
no way to tell them apart.

```go
var Steps = []Step{
    {To: 2, Describe: "...", Blockers: ..., Apply: ...},
}
```

- **Upgrades run automatically, but only when safe.** Each step's `Blockers` inspects the data first
  and returns what stands in the way; `Run` refuses with that list rather than applying. The common
  case is invisible, the case needing a human is loud. This avoids the boot-blocking stop a
  mandatory `joka upgrade` command would put in every `&&` chain.
- **Only mutating commands upgrade.** Read-only commands work fine against a database that is behind
  or blocked — you can always run `joka status` to see what is wrong.
- **A blocked upgrade changes nothing**, including the version stamp, so a retry starts from a state
  that was described. `Apply` must be idempotent.
- **Steps add constraints, never rewrite data.** An upgrade may add an index or a column; it must not
  change what a row means.
- **`meta.PreMarkerVersion`** is what a database with tracking tables but no `joka_meta` reads as.
  It must not default to the current version — that was a real bug in the first draft: every
  un-upgraded database looked upgraded, so no upgrade ever ran.
  `TestPreMarkerDatabaseIsNotMistakenForCurrent` guards it.

### Version 2: _id is the identity of a tracked row

A unique index on `joka_entity_rows.ref_id`, and `ref_id` required. `entity_file` stays on the row
as metadata — where the entity was last declared — and is no longer what identifies it.

Blocked by two things, both real rather than theoretical:

- **Rows with no `ref_id`**, synced before joka recorded one. Fix with `entity reimport` or
  `entity forget`.
- **An `_id` claimed by more than one tracked row.** Version 1 allowed this because it keyed on the
  file, so two entity sets seeded into one database (a `dev1/` and a `local/` tree of the same
  seeds) both claim the same `_id`s. One claim has to go.

`ReimportEntityAction` and `UpdateEntityAction` reject an empty `_id` with `ErrEntitySetInvalid`
rather than writing a row the document cannot key.

### Version 3: entity tracking is one document

`joka_state`, keyed `entities`, holding the `domain.State` JSON. `joka_entities` and
`joka_entity_rows` are read into it and dropped.

It carries the same blockers as version 2, for a different reason: the document is a map keyed on
`_id`, so a row with no `_id` and an `_id` claimed twice are both unrepresentable rather than merely
ambiguous. In practice version 2 has already cleared them — steps run in order and `Run` stops at
the first blocked one — so the checks are there to be honest rather than because they fire.

The step is idempotent by construction: the backend's `Load` prefers the document, so a retry after
a partial run reads what was already written rather than the tables it is in the middle of replacing.

Version 2's unique index now lives for microseconds — step 3 drops the table it is on. It stays
because removing a step renumbers history, which is the one thing the version marker exists to
prevent. Its blockers are what carry the value.

## Entity state (`domain.State`, `app.StateBackend`)

What joka last applied to one database, as one value rather than a set of queries.
`proposal_entity_convergence_20260918.md` is the plan this belongs to; it exists because the tracking
tables were never one artifact — their shape and meaning lived across six commands, each reading the
columns it happened to need, which is how three undocumented format changes got in.

```go
type State struct {
    Version  int                    // domain.StateVersion
    Files    map[string]FileState   // path → content hash
    Entities map[string]EntityState // _id → table, pk, file, position, baseline
    Unkeyed  []TrackedRow           // rows with no _id
}
```

- **`EntityState.Columns` is the baseline**: the SHA-256 of every value joka last applied, keyed by
  column. It is the third point a merge needs — with the declaration and the live row it says which
  side moved, where two points can only say that they differ. Recorded on every insert and on every
  update, because the row was just rewritten.
- **It stores hashes, not values**, for three reasons in order of weight. Secrets: `resolveColumns`
  turns `{{ asm.… }}` into the actual secret before inserting, and the planner goes out of its way
  never to materialize one — storing values would undo that and put Secrets Manager plaintext into
  any state anyone copies. Size: 64 bytes a column whatever it holds, so a large JSON column does
  not bloat the document. And nothing needs the value back, because both sides a prompt would show
  are read live.
- **`app.HashValue` renders through `normalizeValue`**, the same rendering the plan compares with, so
  a baseline and a diff cannot disagree about what a value is.
- **A nil baseline reads as "unknown"** — a row written before it existed — and behaves exactly as
  joka did before: every difference is a conflict.

- **Nothing is keyed on the file.** An entity moves between files, so `EntityState.File` records where
  it was last declared and nothing matches on it. `RowsInFile` exists for the places a file is still
  the unit of work — reimport's deletion order, forget's plan, the diff's alignment.
- **`Order` survives for one reason.** Reimport deletes a file's rows in reverse so children go before
  parents, and a foreign key makes that ordering load-bearing. It is not an identity.
- **Row ordering is total.** `RowsInFile`/`AllRows` sort by file, then position, then `_id`. Position
  is not unique within a file — only a dirty file's rows are re-numbered — so without the last key the
  output moves between runs on the same data.
- **`Unkeyed` carries what cannot be represented.** A row written before joka recorded a `ref_id` has
  no key, and dropping it would lose the row it points at. It is carried so a reader can report it;
  resolving it is the tracking version 2 upgrade's job.
- **A duplicate `_id` is refused** (`ErrStateAmbiguous`). Two rows under one `_id` cannot be one map
  entry, and picking one would be worse than saying so. Unreachable after the version 2 unique index;
  reachable on a database whose upgrade is blocked on exactly this.

`StateBackend` is `Load`/`Save` of the whole document, because that is the access pattern every caller
already has. `infra.PostgresStateBackend` stores it as one jsonb value in `joka_state`, keyed
`entities`. `Save` is one statement, so it is atomic on its own and inside the caller's transaction
it commits with the rows it describes. Dropping tracking happens by removing a map entry, never by
failing to mention one, because a caller saves the document it loaded.

**It reads two layouts.** Version 3 and later store the document; versions 1 and 2 decomposed the
same information across `joka_entities` and `joka_entity_rows`, and a database still on version 2 is
read from those. The document wins when both are present — that is a database mid-upgrade, or one
whose `DROP` did not land, and the document is what the current joka wrote. The legacy reader exists
because **read-only commands never upgrade**, so `joka status` must describe a database no mutating
command has reached.

Only the database backend can write the document in the same transaction as the rows it describes,
which is why it is the default and the only one implemented.

### The state file

After a write commits, the document is materialized to `joka.state.json` in the directory joka was
run from — `joka.<profile>.state.json` when `--profile` is set. That is the terraform arrangement:
state beside the configuration it applies.

**The profile is in the name because one directory syncs several databases.** Without it,
`--profile dev1` would overwrite the state describing `local`, and the next local sync would find its
entities untracked and insert a second copy of every one of them.

**It is not there for durability.** The database backend is transactional and a file is not. It is
there because state that lives inside the database it describes is always self-consistent, so it can
never report that this is the wrong database, or the right one restored from an older dump. Two
markers in `joka_meta` make that comparison possible:

| Key | |
|---|---|
| `state_identity` | a UUID naming this database, stamped once with `ON CONFLICT DO NOTHING` and never rewritten — it travels with a dump, which is the point |
| `state_version` | incremented in the same transaction as the write, because a count that could commit without the write it counts is worse than no count |

`app.AuditState` reads the pair against the file's and returns one of six verdicts; `joka status`
prints the note when it is not `agrees` or `untracked`.

| | |
|---|---|
| `agrees` | normal |
| `untracked` | nothing has written state here |
| `no_file` | the database has been synced, but not from this directory |
| `database_behind` | restored from a dump, or rolled back — the tracking cannot see this, because it rolled back too |
| `file_behind` | a sync ran elsewhere, or did not finish writing |
| `different_database` | `DATABASE_URL` points somewhere unintended |

**Writing is after the commit and cannot fail the command.** A file cannot join a transaction. The
database is already consistent; an unwritten file is a finding `joka status` reports, not a reason to
claim a sync that happened did not. The write goes to a temporary file beside the target and is
renamed, so a reader never sees half a document.

The database copy is still authoritative — the file is a materialized audit copy.
`proposal_entity_convergence_20260918.md` D5 has it becoming a journal that is expunged once the file
is written, which is not done.

### Who loads and who saves

- **Read-only commands** (`joka status`, `entity status`, `entity diff`) load once on the raw
  connection and pass the value to the actions. The actions take a `*domain.State`, not a backend, so
  they cannot write and cannot create a table — which is what keeps `joka status` read-only.
- **Writing commands** load *twice*: once outside the transaction for the preview, and once inside it
  in the action that writes. `entity sync` previews with `PlanSyncAction` on the outer read and
  applies with `ApplySetAction` on the inner one. Making the apply consume the previewed plan instead
  is `proposal_entity_convergence_20260918.md` D10, and is not done.
- **`entity forget` is the exception**: every target edits the one document and the command saves it
  once inside a transaction, so forgetting three orphans is one write rather than six statements with
  nothing around them.
- **`app.DBAdapter` carries no tracking at all** — seven methods, all of them operations on the
  seeded tables (`InsertRow`, `UpdateRow`, `DeleteRow`, `GetRow`, `TableExists`, `RowExists`,
  `LookupValue`). Reading or writing what joka tracked goes through the backend, so there is one
  implementation of that SQL rather than two.
- **Reimport and update require an `_id` on every re-inserted row** (`ErrEntitySetInvalid`). The
  PostgreSQL adapter's `RecordEntityRow` used to enforce that and the actions now do, which means the
  unit tests see the same refusal the database gave.

## Convergence (`entity sync`)

The seed files are the desired state. Every declared entity is compared against the database,
whether or not its file changed — that is what makes a row deleted or edited out of band visible at
all, and it is the finding `--force` was added for and never fixed.

`app.ClassifyColumn` compares three points:

| declared vs baseline | live vs baseline | verdict |
|---|---|---|
| same | same | unchanged |
| changed | same | **push** — only the file moved |
| same | changed | **conflict** — the database moved |
| changed | changed | **conflict** — both moved |

- **A nil baseline reads as push, not conflict.** The absence of a record is not evidence that the
  database moved, and treating it as one would make the first sync after the version 3 upgrade a
  wall of conflicts on a database where nothing is wrong. Push is what joka did before the baseline
  existed, so an un-baselined row behaves as it always has and gets a baseline on the way through.
- **`HashValue` canonicalises JSON**, the way `valuesEqual` does. PostgreSQL renders `jsonb` in its
  own key order with a space after each colon, so hashing raw text made every JSON column's baseline
  differ from the value it was taken from.
- **A non-deterministic column gets half the comparison, and it is the important half.**
  `{{ now }}`, `{{ argon2id|… }}` and `asm.*` re-resolve to a new value every run, so
  declared-against-baseline always reads as changed and says nothing. But the baseline holds the
  hash of what joka **actually inserted** — a concrete timestamp, a concrete argon2id digest — so
  live-against-baseline says exactly whether the database moved. A password somebody reset is drift
  joka can see (`appendRegenerated`):

  | live vs baseline | |
  |---|---|
  | same | the database holds what joka wrote — rewrite only when the file changed, or every boot churns every `created_at` |
  | different | the database moved: conflict |
  | no baseline | nothing to compare against; fall back to the file hash |

  Neither side's value is ever shown for these. A fresh hash tells the reader nothing, and an
  `asm.*` secret must not be printed — the planner goes out of its way not to resolve one. The
  conflict names the column and carries `LiveHash` so conceding it can record the right baseline.
- **The content hash no longer gates the comparison.** It decides which files get their hash
  rewritten, and it carries the "did the author change this" signal for the non-deterministic case
  above.
- **`--force` is gone.** It existed because the hash was the gate.

### `--on-conflict`

| | |
|---|---|
| `fail` (default) | report, write nothing, exit non-zero — the drift gate, as `migrate verify` is for schema |
| `file` | the declaration wins; write over the database's values |
| `db` | the database wins; leave the column and record what it holds as the new baseline |

`db` does not rewrite the YAML — that is `entity resolve`, which is not built. It only stops joka
fighting the database, by conceding the baseline so the same difference is not reported again.

`ApplySetAction` takes the decision as `Keep` (`_id` → column → live hash) rather than a policy
enum, so the applier has one concept — "these columns are the database's" — and the same field
carries a per-column answer when the interactive resolve lands.

## Identity matching (`ApplySetAction`)

`entity sync` matches a declared entity to its tracked row on `_id`, across the whole set. This
replaced matching on (file, position), which is what `AlignTrackedRows` and `ErrStructuralChange`
existed for — both are gone.

What each edit costs now:

| Edit | Before | Now |
|---|---|---|
| add an entity | `ErrStructuralChange` → reimport deleted every row the file owned | one INSERT |
| remove an entity | same | reported as undeclared; nothing deleted |
| reorder entities | refused when keyed, **silently swapped row contents** when not | nothing but re-recorded positions |
| rename a file | orphaned every row, re-inserted duplicates | rows re-pointed; the old record is cleared |
| move an entity between files | same | same |

### Design notes

- **The whole set is read, not one file.** An entity can move between files, so the row it
  corresponds to may be tracked against a file other than the one declaring it. `GetAllTrackedRows`
  is what makes that visible; a per-file read cannot.
- **`refMap` starts pre-populated from every tracked row.** A `{{ ref.id }}` resolves whether its
  target is written this run or was written some previous one — which also makes cross-file
  references work. Files are processed in load order, so a reference resolves when its target's file
  sorts first, the same rule as within a file.
- **An unchanged file is still read.** It contributes its declarations, which is what makes an `_id`
  claimed elsewhere and an entity declared nowhere both visible. Only dirty files are written.
- **Nothing is deleted for an undeclared entity.** A seed file edited by mistake must not take data
  with it. They are reported, with `entity forget` and `entity diff` named.
- **A table change is refused** (`ErrEntityTableChanged`). An `_id` names one row; the same `_id` on
  a different table is a different thing wearing the same name, and guessing would be worse.
- **`forgetEmptyFiles`** drops the `joka_entities` record of a file that is gone and whose every row
  moved elsewhere. Without it a rename leaves a permanent ghost nothing but a manual forget clears.
- **The plan runs before the early return.** A run with no dirty files can still have something to
  report: deleting a file leaves every other file unchanged, and its entities declared nowhere.
  Found by testing a deletion, not by review.

### Transaction discipline

Identity matching made a latent bug reachable and it is now fixed: `joka_entities` DML and every
tracked-row read run on the adapter's `DBTX` handle, not the raw connection. A run that re-points a
row and then asks which files still hold rows must see its own writes, and a rolled-back sync must
not leave a file recorded as synced with no rows behind it. Only DDL (`Ensure*`) still uses the raw
connection, because DDL cannot run in the transaction.
