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

Create a `.jokarc.yaml` in your project root to configure paths:

```yaml
migrations: devops/migrations
entities: devops/entities
statefile: joka.state.json    # optional; see "State file" below
```

All fields are optional. CLI flags override `.jokarc.yaml` values. If neither is provided, defaults
apply (`devops/migrations`, `devops/entities`, and `joka[.<profile>].state.json` beside the working
directory).

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

Entity files and the rows they wrote are recorded in one JSON state document, stored in `joka_state` and materialized to `joka.state.json` beside the working directory. Every declared entity is compared against the database on every sync; the file's content hash decides which files get their hash rewritten, not what gets reconciled.

## Commands

### `joka init`

Creates the `joka_migrations` tracking table. Run this once before your first migration.

### Tracking version

joka records what wrote a database's bookkeeping in a `joka_meta` table:

| Key | Meaning |
|---|---|
| `tracking_version` | The shape and meaning of the `joka_*` tables. A single integer, bumped only when a change would make an older joka misread them |
| `joka_version` | The joka release that last wrote here |

Any command that writes stamps both. Read-only commands (`entity diff`,
`migrate status`, `migrate verify`) never create the table.

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
  go. No joka command can do it: every one that writes is gated on this upgrade,
  and reading the tracking fails on the same ambiguity. Drop the losing claim
  directly, e.g. DELETE FROM joka_entity_rows WHERE entity_file = '<file>'
```

Read-only commands (`entity diff`, `migrate status`, `migrate verify`) work
normally against a database that is behind or blocked, so you can always look
before deciding.

| Version | What changed |
|---|---|
| 1 | The tracking tables as of v0.13.0. The assumed version of any database with no `joka_meta` |
| 2 | `joka_entity_rows.ref_id` is unique and required — `_id`, not file and position, identifies a tracked row. `entity_file` becomes metadata recording where the entity was last declared |
| 3 | Entity tracking becomes one JSON document in `joka_state`; `joka_entities` and `joka_entity_rows` are read into it and dropped |


A database with tracking tables but no `joka_meta` predates the marker — it is
read as the current version and stamped on the next command that writes.

### `joka migrate new <name>`

Creates a new timestamped migration file in the migrations directory. It writes
a file and never opens a connection, so it works without a reachable database.

```bash
joka migrate new create_users_table
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

### `joka entity sync`

Syncs entity YAML files to the database, and is the only command that seeds data. Every declared entity is compared against the database whether or not its file changed: an entity joka does not track is claimed if a unique key finds its row and inserted otherwise, and a tracked one has each declared column compared against the file, the database, and what joka last applied. Existing primary keys are preserved, so external rows referencing them stay valid. Runs in a transaction with advisory locking.

Adding, removing, reordering or moving an entity between files costs nothing: entities are matched to their rows by `_id` across the whole set. An `_id` tracked against one table and declared on another is refused, because the same `_id` on a different table is a different thing wearing the same name.

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

An entity that no file declares any more is deleted — the declaration is the
desired state, so an entity it no longer mentions is one joka is being told to
stop owning. The plan names every row first, and the confirmation is the gate:

```
Declared in no file any more — these rows will be DELETED:
  - field_ceo  fields id 3  (last declared in 03_ceo.yaml)
  - field_ceo_v1  field_versions id 3  (last declared in 03_ceo.yaml)

Proceed with entity sync? (only 'yes' will confirm):
```

A tracked row with no `_id` is the exception: joka cannot match it to a
declaration at all, so "no file declares it" is not something it knows. Those
are reported and left alone.

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

`joka entity diff` names them per file, so you can see them without
running a sync.

**Scope is the entity set joka loads for this run** — whatever `entities:`
resolves to after the profile overlay — not the whole filesystem. Parallel
per-environment trees (`devops/entities/local`, `devops/entities/dev1`) share
`_id`s on purpose: they are the same logical entity for different environments,
and only one set is ever loaded. An `_id` is therefore unique per database, not
universally.

### `joka entity diff <file>`

Lines an entity file's declared graph up against the rows joka tracks for it and
the rows that are actually in the database. Read-only: no lock, and it creates
none of the `joka_*` tables.

It is the no-risk half of `entity sync`: it predicts what a sync would do
without doing any of it. A `+` is a row that would be inserted, a `@` one that is
already in the database and would be claimed, a `≠` one whose columns would be
rewritten.

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

  6 declared · 4 tracked · 2 insert · 0 claim · 0 delete · 1 changed · 2 moved

  → joka entity sync   (updates 1 row in place)
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

# which rows already in the database would it claim, and on what key?
joka entity diff system_fields.yaml -o json |
  jq '.lines[] | select(.status=="adopt") | {ref_id, pk_value, matched_on}'
```

### `joka unlock`

Force-releases an advisory lock left behind by a crashed process. Shows who held the lock before releasing it.

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--env` | `-e` | `.env` | Path to the environment file |
| `--profile` | `-p` | | Config profile to use (from `.jokarc.yaml` `profiles:`) |
| `--migrations` | `-m` | `devops/migrations` | Path to the migrations directory |
| `--entities` | | `devops/entities` | Path to the entities directory |
| `--auto` | `-a` | `false` | Skip confirmation prompts |
| `--output` | `-o` | `text` | Output format: `text` or `json` |
| `--up-to` | | | Migration index to consolidate up to (required for `migrate consolidate`; must be the last applied migration) |
| `--dry-run` | | `false` | Print the plan and exit without applying (`entity sync`) |
| `--statefile` | | | Path to the state file (default: `joka[.<profile>].state.json` beside the working directory) |
| `--on-conflict` | | `fail` | What to do when the database changed since joka last wrote: `fail`, `file`, `db` or `ask` (`entity sync`). `db` and `ask` rewrite the seed files where the database wins |
| `--decayed` | | `false` | Treat the seeded data in the database as stale: rewrite every declared column and report no conflicts (`entity sync`) |

## How It Works

Joka uses five internal tables (all prefixed with `joka_`):

- **`joka_migrations`** — Tracks which migrations have been applied and when.
- **`joka_lock`** — Advisory lock table (at most one row). Prevents concurrent `migrate up` or `entity sync` runs.
- **`joka_snapshots`** — Stores a full schema snapshot (JSON of all `CREATE TABLE` statements) after each migration is applied.
- **`joka_state`** — One JSON document holding everything joka has seeded: each entity file's content hash, and each `_id` with the row it became and a per-column baseline of the values last applied.
- **`joka_meta`** — The tracking format version, the joka release that last wrote, and the identity of this database.

All of these except `joka_migrations` are created automatically on first use. Only `joka_migrations` requires `joka init`.

### The state file

After a write commits, joka materializes its state to `joka.state.json` in the directory it was run
from — `joka.<profile>.state.json` when `--profile` is set, because one directory often syncs several
databases and a shared name would have each overwrite the last. `--statefile`, or `statefile:` in
`.jokarc.yaml`, overrides both.

**The database copy is authoritative.** The file is an audit copy, and losing it costs nothing: joka
reads what it applies from `joka_state`. What the file adds is a second opinion, because state living
inside the database it describes is always self-consistent and can never report that this is the
wrong database.

Two markers in `joka_meta` make that comparison possible: an identity stamped once per database and
never rewritten (so it travels with a dump), and a counter incremented with each write. If the file's
identity and the database's disagree, **every command that writes refuses**:

```
the state file describes a different database: joka.state.json names 63e9ba27…,
and this database is a1b2c3d4…. Nothing was written.
  If the connection is right, the state file is stale: remove it, or point
  --statefile somewhere else.
```

Only a disagreeing identity refuses. A version disagreement is informational — joka loads what it
applies from the database, so a file that is ahead or behind does not change what a run does. A
database with no state at all, which is what `joka drop` leaves, is not a disagreement either.

Whether you commit the file is your call. It is per-environment, so it belongs beside the other
per-environment configuration if you keep it.

### Primary key gaps

A joka run that fails leaves gaps in `serial` / `identity` primary keys. Every run is
wrapped in a transaction, so a failure rolls back cleanly and no row survives it — but
PostgreSQL sequences are deliberately exempt from rollback, so the numbers those inserts
consumed are not returned:

```
-- one committed insert, two rolled back, one committed
SELECT string_agg(id::text, ',' ORDER BY id) FROM t;   -- 1,4
```

This is normal PostgreSQL behaviour and not specific to joka: anything that inserts inside
a transaction that later rolls back does the same. It is called out here only because seed
data is the one place people tend to expect tidy, contiguous ids — nothing in joka depends
on them being contiguous, and neither should anything else.

Tools that avoid this run migrations against a throwaway *shadow* database first (Atlas
works this way). joka does not, deliberately: it would mean provisioning and maintaining a
second database to make primary keys look neater, which is a large amount of machinery for
a cosmetic property.
