# Joka

Joka is a database migration and data management tool for PostgreSQL. It tracks and applies SQL migrations, captures schema snapshots, syncs seed data from files to database tables, and seeds entity graphs with parent-child relationships.

<p align="center">
  <img src="joka.jpg" alt="joka" width="400">
</p>

## Install

Build from source (requires Go 1.25+):

```bash
go install github.com/apsdsm/joka@latest
```

`joka migrate consolidate` additionally needs `pg_dump` on `PATH`. No other command requires an external binary.

> **MySQL support was removed in v0.14.0.** joka is PostgreSQL only. A non-PostgreSQL `DATABASE_URL`, or a `connection.driver` other than `postgres`, is refused with an error naming the removal rather than failing as a connection problem. To stay on MySQL, pin v0.13.0.

## Setup

### Database URL

Joka needs a database connection string. Either add a `.env` file in the directory you run from, or set `DATABASE_URL` as an environment variable. You can point to a specific env file with `--env`.

```
DATABASE_URL=postgresql://user:pass@localhost:5432/my_db?sslmode=disable
```

The URL must start with `postgres://` or `postgresql://`.

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
  url: postgresql://root:root@localhost:5432/my_db   # full DSN, used verbatim
# --- or ---
connection:
  driver: postgres
  host: localhost
  port: 5432
  user: root
  database: my_db
  password: root                              # inline password; joka builds a URL-safe DSN
```

**AWS Secrets Manager** — resolve the DSN from a secret at runtime:

```yaml
connection:
  source: aws_secrets_manager   # "env" (default) | "aws_secrets_manager"
  driver: postgres              # optional; postgres is the only value
  host: 127.0.0.1
  port: 5432
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
      driver: postgres
      host: 127.0.0.1
      port: 5432
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

### `joka status`

One read-only report of the three states joka works between:

| Plane | What it is |
|---|---|
| declared | the devops folder — migration files, entity YAML, template records |
| tracked | joka's own tables — `joka_migrations`, `joka_snapshots`, `joka_entities`, `joka_entity_rows`, `joka_meta` |
| live | the database — tables, columns, rows |

Every mismatch joka can hit is a disagreement between two of those. The other
commands each cover one edge (`migrate status`: declared vs tracked;
`migrate verify`: tracked vs live; `entity status`: declared vs tracked) in
their own format; `joka status` covers all of them on one screen, and covers two
edges no other command reports: whether the rows joka tracks for an entity file
are still in the database, and whether template files and their tables agree.

```
joka status   postgres · profile dev-remote
declared = the devops folder · tracked = joka_* tables · live = the database

MIGRATIONS  devops/migrations
  migration                    declared  tracked  status
  250116140000_add_users              ✓        ✓  applied
  250826094500_add_ceo_field          ✓        ·  pending
  1 applied · 1 pending

  schema drift vs snapshot 250116140000 — 2 tables differ
    + audit_log
      in live, not in the snapshot — DDL applied outside a migration
    ~ fields
      live only:     "label_ja" character varying(255)

ENTITIES  devops/entities
  file                              declared  tracked  live  status
  01_clients/jjc2_admin.yaml               4        4     4  synced
  04_fields/system_fields.yaml            48       38    38  modified
      sync would refuse: 04_fields/system_fields.yaml now defines 48 entities
      but 38 are tracked (an entity was added or removed)
      every declared entity and tracked row carries an _id
  08_slots/system_assignments.yaml         ·       12     0  orphaned
      12 rows tracked for this file are not in the database
      table entity_slot_assignments no longer exists
  1 synced · 1 modified · 1 orphaned

TEMPLATES  devops/templates
  table            strategy  files  declared  live
  email_templates  truncate      3         3     3
  settings         truncate      2        12     9  differs

LOCK
  not held

ACTIONS
  migrations  joka migrate up
      1 pending migration
  entities    joka entity reimport 04_fields/system_fields.yaml
      04_fields/system_fields.yaml now defines 48 entities but 38 are tracked
  entities    joka entity forget 08_slots/system_assignments.yaml
      tracked with 12 rows but the file is gone; forget drops the tracking and
      leaves any surviving rows alone
```


`--compact` prints the same report as a single line, for CI logs, shell prompts
and the head of a startup chain:

```
$ joka status --compact
joka  migrations 3/4  drift 2  entities 19/20 +1 !1  templates 2/3  → 5 actions
```

The segments are positionally stable — every section appears whether or not it
has a problem — so the line reads the same way every time. `+N` counts entity
files that are new or modified; `!N` counts real problems (orphans, missing
rows, structural refusals, parse failures). `n/a` means the section could not be
read, usually a tracking table that does not exist yet. A held lock adds `lock
held`. Exit code is 0 either way; `--compact` and `--output json` are mutually
exclusive, since the JSON is already the whole report.

Notes on reading it:

- **`·` means "does not apply"**, not zero. A declared count of `·` is a file
  that is not on disk; a real zero prints as `0`.
- **`sync would refuse`** comes from the same check `entity sync` runs, so the
  verdict always matches what a real sync would do.
- **The template comparison is only exact for the `truncate` strategy.** An
  `update` table can legitimately hold rows no file declares, so its counts are
  reported without being flagged.
- **Actions with `(nothing joka can run)`** are findings joka has no command
  for. There is currently no such finding for entities — `entity forget` covers
  the orphan and the deleted-row cases — but the report will print it rather
  than name a command that does not exist.
- **`status` writes nothing.** Unlike the other commands it does not
  auto-create the `joka_*` tracking tables, because a missing tracking table is
  one of the things worth reporting. It also does not acquire the advisory lock.
- **Exit code is 0** whenever the report could be built, including when it finds
  problems. Use `--output json` and read `actions` for CI gating; `migrate
  verify` remains the command that exits non-zero on schema drift.

`--output json` emits the whole report as one object, with `actions` as a list
of `{scope, subject, reason, command}`. Empty lists are `[]` rather than `null`.

```bash
# anything to do?
joka status -o json | jq '.actions | length'

# which entity files have tracked rows missing from the database?
joka status -o json | jq '.entities.files[] | select(.missing_rows > 0) | .path'
```

### Tracking version

joka records what wrote a database's bookkeeping in a `joka_meta` table:

| Key | Meaning |
|---|---|
| `tracking_version` | The shape and meaning of the `joka_*` tables. A single integer, bumped only when a change would make an older joka misread them |
| `joka_version` | The joka release that last wrote here |

Any command that writes stamps both. Read-only commands (`status`, `entity
diff`, `migrate status`, `migrate verify`, `entity status`) never create the
table.

The point is the case that cannot be detected any other way: **an older joka
pointed at a database a newer joka has already written.** It has no way to know
the format moved, so it would read the new shape as the old one. joka refuses
instead:

```
Error: database bookkeeping is newer than this joka: the database is at tracking
version 2 (written by joka 0.15.0), this joka understands 1 — upgrade joka
```

Upgrades to the tracking format run automatically the first time a command
writes, and only when they are safe:

```
$ joka entity sync
Upgraded tracking to version 2: make _id the identity of a tracked row
```

When something in the data stands in the way, joka refuses and says what,
changing nothing:

```
Error: tracking upgrade is blocked: cannot make _id the identity of a tracked row
  these _ids are claimed by more than one tracked row: role_owner (2 rows, in
  local/a.yaml and dev1/a.yaml). An _id identifies one row, so one claim has to
  go — 'joka entity forget <file>' drops a file's tracking without touching its rows
```

Read-only commands (`status`, `entity diff`, `entity status`, `migrate status`,
`migrate verify`) work normally against a database that is behind or blocked, so
you can always look before deciding.

| Version | What changed |
|---|---|
| 1 | The tracking tables as of v0.13.0. The assumed version of any database with no `joka_meta` |
| 2 | `joka_entity_rows.ref_id` is unique and required — `_id`, not file and position, identifies a tracked row. `entity_file` becomes metadata recording where the entity was last declared |


A database with tracking tables but no `joka_meta` predates the marker — it is
read as the current version and stamped on the next command that writes.
`joka status` shows the marker in its header.

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

Squashes the applied migration history into a single baseline file, dumped by `pg_dump`.

Joka does not write the schema itself. It shells out to `pg_dump` — the reference implementation — because anything joka reconstructed by hand would silently lose whatever it did not know about. Joka's job here is bookkeeping: write the baseline, remove the tracking rows it replaces, delete the files it supersedes.

**Requires `pg_dump` on `PATH`.** It also refuses to run against a server newer than itself, so keep the client version at or above the server's.

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

**What the baseline contains.** Everything `pg_dump --schema-only` emits: tables, indexes, constraints, sequences, identity and generated columns, types, domains, views, materialized views, functions, triggers, extensions. The `joka_*` tracking tables are excluded, along with the sequences they own. `\restrict` / `\unrestrict` psql directives are stripped, since they are client commands rather than SQL. Foreign keys arrive as trailing `ALTER TABLE` statements, so table order does not matter.

**Checks before anything is deleted.** The dump runs first; a missing binary, a version mismatch, or a dump that does not contain one `CREATE TABLE` per table in the database all abort before any file is written or removed.

### `joka data sync`

Syncs template/seed data from files to database tables based on the `tables` config in `.jokarc.yaml`. Currently supports the `truncate` strategy (delete all rows, then insert from files). Runs in a transaction with advisory locking.

### `joka entity sync`

Syncs entity YAML files to the database. New files have their entity graph inserted depth-first (parents before children), resolving template expressions along the way. Files that changed since the last sync (`[modified]`) are reconciled **in place**: each entity is updated by primary key against the tracked row at the same depth-first position — existing PKs are preserved (no delete, so no FK conflict) and entities without an `_id` are handled fine. Unchanged files are skipped. Runs in a transaction with advisory locking.

If a modified file changed structurally (a different number of entities than tracked, an entity's table changed, or an `_id` that disagrees with the tracked row at that position), sync refuses to guess and recommends `entity reimport` instead.

**Matching is by `_id`, across the whole set.** A declared entity is matched to
the row tracked under the same `_id`, wherever that row was declared before:

| Edit | What sync does |
|---|---|
| add an entity | one INSERT; everything else keeps its primary key |
| remove an entity | reports it as no longer declared. **Nothing is deleted** |
| reorder entities | nothing, beyond re-recording the new positions |
| rename a file | re-points the rows and clears the old file's record |
| move an entity between files | the same — the row does not change |
| change an entity's `_is` | refused: an `_id` names one row, and a different table is a different thing |

Because every tracked row's primary key is known before the run starts, a
`{{ ref.id }}` resolves whether its target is written this time or was written
previously — including a reference to an entity declared in another file. Files
are processed in load order, so the target's file has to sort first, the same
rule as within a file.

An entity that no file declares any more is reported, never deleted — a seed
file edited by mistake should not take data with it:

```
2 tracked entities are no longer declared in any file:
  field_ceo  fields id 3  (last declared in 03_ceo.yaml)
  field_ceo_v1  field_versions id 3  (last declared in 03_ceo.yaml)

  Nothing was deleted. 'joka entity forget <file>' drops the tracking,
  'joka entity diff <file>' shows what each one points at.
```

Before applying, sync prints a plan — new files show the rows to be inserted, and modified files show a per-column before/after diff. Use `--dry-run` to print the plan and exit without changing anything (and without taking the advisory lock). Non-deterministic columns like `{{ argon2id|… }}` and `{{ now }}` are shown as `(regenerated)`; secret-backed columns (`{{ asm.… }}`, hashed or plain) are also redacted this way and are never fetched or displayed at plan time. A `{{ lookup|… }}` whose target row doesn't exist yet (e.g. it's inserted by another file in the same sync) is shown as `(lookup, resolved at apply time)` rather than failing the plan. With `--output json`, the plan is included as a `plan` object.

```bash
# see exactly what a sync would change, without applying
joka entity sync --dry-run
```

### Entity identity

Every entity must declare an `_id`, and no `_id` may be claimed twice:

```yaml
entities:
  - _is: fields
    _id: field_company_ceo     # required — identifies this row
    xid: fld_0000000000000001
```

`joka entity sync` validates the whole set before writing anything and refuses
if either invariant is broken, naming every problem at once:

```
Error: entity set is not valid: 1 entity without an _id and 1 _id claimed twice
  no _id: a.yaml entity #2 (fields)
  _id "alpha" is claimed by:
    a.yaml entity #1 (fields)
    b.yaml entity #1 (fields)
```

`joka status` reports the same problems per file, so you can see them without
running a sync.

**Scope is the entity set joka loads for this run** — whatever `entities:`
resolves to after the profile overlay — not the whole filesystem. Parallel
per-environment trees (`devops/entities/local`, `devops/entities/dev1`) share
`_id`s on purpose: they are the same logical entity for different environments,
and only one set is ever loaded. An `_id` is therefore unique per database, not
universally.

### `joka entity status`

Shows the sync status of each entity file: `synced` (hash matches), `modified` (file changed since last sync), `new` (not yet synced), or `orphaned` (tracked but file deleted). Uses SHA-256 content hashing.

### `joka entity diff <file>`

Lines an entity file's declared graph up against the rows joka tracks for it and
the rows that are actually in the database. Read-only: no lock, and it creates
none of the `joka_*` tables.

It exists because sync's refusal names the symptom and not the change:

```
Error: entity file changed structurally; use 'entity reimport':
system_fields.yaml now defines 6 entities but 4 are tracked
```

That says nothing about which entities are new, and the remedy it names deletes
every row the file owns. `entity diff` shows the shape of the change instead:

```
entity diff  system_fields.yaml
matched by _id — every declared entity and tracked row carries one

      #  table              _id                    tracked  row
  =   1  fields             field_company_name           1  id 1
  =   2  └─ field_versions  field_company_name_v1        2  id 1
  +   3  fields             field_company_ceo            ·  ·
  +   4  └─ field_versions  field_company_ceo_v1         ·  ·
  ≠   5  fields             field_company_addr           3  id 2
      label  Address → Registered address
  ~   6  └─ field_versions  field_company_addr_v1        4  id 2

  6 declared · 4 tracked · 2 insert · 0 delete · 1 changed · 2 moved
  positional alignment breaks at declared #3

  entity sync would refuse this file:
      entity file changed structurally; use 'entity reimport': …
  every entity and tracked row carries an _id, so an identity match would be exact
  → joka entity reimport system_fields.yaml   (deletes and re-inserts every row)
  → joka entity update system_fields.yaml    (inserts the 2 new rows, leaves
    existing rows untouched — the 1 column that changed would NOT be applied)
```

The `_has:` nesting is drawn as a tree: a child entity sits under its parent,
with `├─` and `└─` marking siblings. The rows are still listed in the flat,
depth-first order they are inserted and tracked in — the tree only shows which
entity each row belongs to, which is otherwise invisible once the graph is
flattened.

Markers:

| | Meaning |
|---|---|
| `=` | declared and tracked agree |
| `≠` | matched, but some columns differ — the columns are listed underneath |
| `+` | declared, not tracked — sync would insert it |
| `-` | tracked, not declared — the file no longer defines this row |
| `~` | matched at a different position, values unchanged |
| `!` | positional matching paired two rows in different tables |

**Matching.** When every declared entity and every tracked row carries an `_id`,
the two sides are matched on it and the header says so. Otherwise it falls back
to position — the same thing sync does — and names the entities and rows with no
`_id`, which is what is stopping an identity match. `positional alignment breaks
at declared #N` is the point where walking both sides in step stops describing
the same row: the position sync's own matching would start writing to the wrong
one.

**Column values** are compared by default, one query per matched row. A row that
is not in the database is not compared (there is nothing to compare against) and
says so. `--no-values` skips the comparison entirely.

Two things are deliberately **not** reported as differences:

- **JSON columns whose keys are merely in a different order.** PostgreSQL renders
  `jsonb` in its own key order with a space after each colon, while the YAML
  carries whatever the author typed. Both sides are canonicalised before
  comparing (and before display), so an untouched `jsonb` column stays quiet and
  a real change shows with both sides in the same key order. `entity sync
  --dry-run` gets the same treatment.
- **Columns rewritten on every sync** — `{{ now }}`, `{{ argon2id|… }}`, an
  `asm.*` secret. These are a property of the file, not a difference from the
  database, so they are listed once in the summary rather than marking every row
  changed. A single `created_at: "{{ now }}"` would otherwise light up every row
  in the file, forever.

`--output json` returns the whole alignment, including `matched_by`,
`keyed_by_id`, `positional_break`, `sync_verdict`, and per line
`{status, moved, declared_pos, tracked_pos, table, ref_id, pk_value, live,
table_missing, changes}`.

```bash
# which entities would a sync insert?
joka entity diff system_fields.yaml -o json | jq '.lines[] | select(.status=="insert") | .ref_id'

# would an _id-keyed match be exact on every file?
joka status -o json | jq -r '.entities.files[].path' |
  xargs -I{} sh -c 'joka entity diff {} -o json | jq -r "\"{} \" + (.keyed_by_id|tostring)"'
```

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

### `joka entity forget <file>` / `joka entity forget --orphans`

Removes joka's tracking for an entity file — its `joka_entities` record and its
`joka_entity_rows` entries — **without touching the rows that tracking points
at, or the file on disk.** The opposite of `reimport`, which replaces the rows
and keeps the tracking.

It is for the two states no other command resolves:

| State | What happened | What forget does |
|---|---|---|
| tracked, rows gone | the rows were deleted by hand elsewhere, so tracking points at nothing | drops the tracking; `entity sync` then treats the file as new |
| orphaned | the file and its rows were both deleted, but the tracking outlived them | drops the tracking; nothing else is left to clean up |

```bash
joka entity forget 01_operators/sysadmin_grants.yaml
joka entity forget --orphans     # every tracked file that is no longer on disk
```

It shows what it will remove and the state of each row before asking to confirm:

```
Entity forget:

  08_slots/system_assignments.yaml
    entity_slot_assignments  id 5  (_id slot_a)  — table no longer exists
    slots                    id 12               — already gone from the database

  Database rows are not touched. Files on disk are not touched.

Forget this tracking? Database rows are not touched (only 'yes' will confirm):
```

**It refuses when the rows are still in the database.** Dropping the tracking
for a live row leaves a row joka does not own, and the next `entity sync` treats
the file as new and inserts a second copy. `--force` overrides:

```
Error: tracked rows are still in the database: 1 of 1; use --force to forget
them anyway, or 'entity reimport' to replace them
```

Retiring a seed file that should stop being applied is two steps, because forget
deliberately does not touch files: forget the tracking, then delete the file or
rename it so discovery skips it (any extension other than `.yaml` / `.yml`
works, e.g. `mv seed.yaml seed.yaml.off`).

`--output json` returns `{"status": "ok", "forgotten": [{file, rows: [{table,
pk_column, pk_value, ref_id, live, table_missing}], live}]}`.

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
| `--up-to` | | | Migration index to consolidate up to (required for `migrate consolidate`; must be the last applied migration) |
| `--ignore-foreign-keys` | | `false` | Defer FK constraint checks during data sync truncate |
| `--dry-run` | | `false` | Print the plan and exit without applying (`entity sync`) |
| `--statefile` | | | Path to the state file (default: `joka[.<profile>].state.json` beside the working directory) |
| `--on-conflict` | | `fail` | What to do when the database changed since joka last wrote: `fail`, `file` or `db` (`entity sync`) |

## How It Works

Joka uses four internal tables (all prefixed with `joka_`):

- **`joka_migrations`** — Tracks which migrations have been applied and when.
- **`joka_lock`** — Advisory lock table (at most one row). Prevents concurrent `migrate up`, `data sync`, or `entity sync` runs.
- **`joka_snapshots`** — Stores a full schema snapshot (JSON of all `CREATE TABLE` statements) after each migration is applied.
- **`joka_entities`** — Tracks which entity files have been synced (with content hashes for change detection).
- **`joka_entity_rows`** — Tracks individual rows inserted per entity file, enabling reimport (delete + re-insert) and update (additive insert).

The lock, snapshot, entity, and entity row tables are created automatically on first use. Only `joka_migrations` requires `joka init`.
