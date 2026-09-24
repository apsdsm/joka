---
name: joka
description: Use joka to manage a PostgreSQL database's migrations and seed data — check whether a database is up to date, apply migrations, sync seed entities, resolve drift between the seed files and the database, adopt an existing database, and rename or remove seeded rows. Use when a project has a .jokarc.yaml or a devops/migrations or devops/entities directory, when the user mentions joka, seed data, seeding, entity sync, migrations in such a project, or when a command has printed a joka error (conflict, drift, wrong database, tracking upgrade blocked).
---

# Using joka

joka keeps one PostgreSQL database matching two things you declare on disk: SQL
migrations, and seed data written as YAML entities. It is terraform-shaped — the
files are the desired state, joka works out the difference and applies it.

**Before anything else, run `joka status`.** It is read-only, creates nothing,
and tells you where you are. Most questions about a joka project are answered by
it.

```
database   tracking v3 · written by joka 0.14.0
state      the state file and the database agree
migrations 29 applied · no drift
entities   6 tracked across 5 files
lock       free
```

## The mental model

Three things exist, and every joka behaviour is about a disagreement between two
of them:

- **declared** — the YAML files in the entities directory
- **live** — the rows in the database
- **tracked** — what joka recorded the last time it wrote (the state document)

For a **column value**, joka compares all three. Only the file moved since joka
last wrote → it writes the file's value. The database moved → that is a
*conflict*, and joka refuses rather than discarding a change it cannot account
for.

For **existence**, there is nothing to arbitrate, so it follows a table:

| in the database | declared | tracked | what happens |
|---|---|---|---|
| ✓ | ✓ | ✗ | the row is **claimed** (adopted) |
| ✓ | ✗ | ✗ | not joka's concern — it never sees it |
| ✓ | ✗ | ✓ | the row is **deleted** |
| ✗ | ✓ | ✓ | it is **inserted again** |
| ✗ | ✗ | ✓ | the **tracking is dropped**, nothing deleted |
| ✓ | ✓ | ✓ | normal: converge the columns |

Every entity is identified by its `_id`, which must be present and unique across
the whole loaded set. That is what lets an entity move between files, be
reordered, or be renamed without losing its row.

## Commands

| | |
|---|---|
| `joka status` | Read-only inventory. Start here. |
| `joka entity sync` | Apply the seed files. The only path by which data is seeded. |
| `joka entity sync --dry-run` | The plan, without applying or taking the lock. |
| `joka entity sync --allow-delete` | Lets a `--auto` / `--output json` run delete rows no file declares. |
| `--wait <duration>` | Retry the connection until the deadline. For container entrypoints. |
| `joka entity diff <file>` | Declared vs tracked vs live, per row, for one file. |
| `joka migrate status` | Applied vs pending. |
| `joka migrate up` | Apply pending migrations — all in one transaction. |
| `joka migrate verify` | Schema drift vs the snapshot. Non-zero exit: this is the CI gate. |
| `joka migrate new <name>` | New timestamped migration file. Needs no database. |
| `joka migrate consolidate --up-to <index>` | Squash history into one pg_dump baseline. |
| `joka init` | Create the migrations table. |
| `joka reset` | **Destructive.** drop → init → migrate up → entity sync. |
| `joka drop` | **Destructive.** Drops every table including joka's own. |
| `joka unlock` | Force-release a lock left by a crashed run. |

`--output json` works on all of them. `-e <file>` picks the dotenv,
`--profile`/`-p` the config profile.

## Workflows

### Find out where a database stands

```bash
joka status
```

Then, if you need detail: `joka entity sync --dry-run` for what a seed sync would
do, `joka migrate status` for the migration chain, `joka entity diff <file>` for
one file row by row.

### Apply seed changes

```bash
joka entity sync --dry-run     # read the plan first
joka entity sync               # prints the plan, then asks to confirm
```

The plan is printed before the confirmation, always. Read it — it names every row
that will be deleted.

### A sync reported a conflict

The database holds a value joka did not write. It exits non-zero and changes
nothing. Decide which side is right:

| | |
|---|---|
| `--on-conflict=fail` | default: report, write nothing, exit non-zero |
| `--on-conflict=file` | the file wins; write over the database |
| `--on-conflict=db` | the database wins; keep its value **and rewrite the seed file to match** |
| `--on-conflict=ask` | prompt per column, rewriting the seed files where the database wins |

`ask` needs a terminal, so it is refused under `--auto` and `--output json`.

**Do not reach for `--on-conflict=file` to make an error go away.** A conflict
means somebody changed that row outside joka. Find out who before overwriting it.

### Start using joka on a database that already has the data

Just sync. joka finds each declared entity by a unique key the declaration fills
in, claims the existing row rather than inserting a duplicate, and records what
the row held so later drift is detectable.

```bash
joka entity sync --dry-run    # rows it would claim are marked @
joka entity sync
```

If a table has no unique index the entity declares, joka cannot tell whether the
row is already there, and inserts — which may then collide. Add a unique
constraint, or declare the columns of one that exists.

### Upgrade a project from joka 0.13

The database needs nothing. `entity sync` migrates the tracking on its own, then refuses because the
seed files have no `_id` — 0.13 matched rows by position, so a 0.13-era project never wrote one:

```
entity set is not valid: 5 entities without an _id
  no _id: api_key.yaml entity #1 (api_keys)
```

Add an `_id` to every entity it names and sync again. Adoption finds each existing row by its unique
key and claims it: no duplicates, nothing deleted. Do **not** wipe `joka_migrations` or the tracking
tables — that throws away the migration history to fix a problem in the YAML.

### Say which root a directory is

A `.jokarc.yaml` may name itself, and the database remembers which name claimed it:

```yaml
root: myproject-prod
```

A second root pointed at the same database is refused rather than allowed to converge it against the
wrong seed files:

```
this database belongs to a different joka root: it belongs to "myproject-local", and this
configuration declares "myproject-prod"
  if you meant to move it, re-run with --adopt-root
```

This is what stops two environment directories sharing one database and each deleting the other's
rows as declared nowhere. It is stronger than the state file, which is usually gitignored and so
absent in CI. It doubles as the environment label: name the root after the environment and a prod
directory pointed at a test database is the same refusal.

- Declaring no root keeps joka behaving exactly as before, so this is opt-in.
- Once a database is claimed, a configuration that declares **no** root is also refused. Add the
  line, or pass `--root`.
- `drop` and `reset` are gated on this too, unlike the other refusals.
- `joka status` names the owner on its database line.

### Rename an entity's `_id`

Usually nothing to do: joka finds the row again by its unique key and re-keys the
tracking. If the rename *also* changes every unique key, say so:

```yaml
moved:
  - from: old_id
    to: new_id
```

`to:` must be declared by some entity and `from:` must not be. A typo in `to:`
would otherwise move the tracking to a name nothing declares, and the next rule
deletes it.

### Stop managing a row without deleting it

Deleting an entity from a file deletes its row. To keep the row and have joka let
go of it:

```yaml
removed:
  - _id: some_id
    keep: true
```

Without `keep:` the row is deleted — the same outcome as undeclaring it, said out
loud in the file.

Both `moved:` and `removed:` are idempotent and silent once applied, so leave them
in place until every environment has run them. joka will never suggest deleting
one; that is your call.

### The seeded data has rotted

When the database's copy of the seed data is not worth reconciling row by row:

```bash
joka entity sync --decayed
```

Rewrites every declared column whatever is there and reports no conflicts.
`_once` columns are still left alone.

## Calling joka from Go

A project that migrates or seeds in its own tests should call the library rather than reimplement
anything joka does:

```go
import joka "github.com/apsdsm/joka/jokalib"

joka.Init(ctx, db)
joka.MigrateUp(ctx, db, "devops/migrations")
joka.EntitySync(ctx, db, "devops/entities")
```

Silent by default; `joka.WithOutput(w)` sends progress somewhere. `joka.WithoutLock()` skips the
advisory lock, which is worth doing against a container the test owns. `EntitySync` fails on a
conflict and deletes rows no file declares, because there is nobody to ask.

**Do not write your own migration splitter.** `db.SplitSQLStatements` is public, and a private copy
will disagree with the tool about something — a semicolon inside a comment, for one.

## Exit codes

| | |
|---|---|
| 0 | nothing to do |
| 1 | joka could not do it, or refused to |
| 2 | there is work to apply |

`migrate status`, `migrate verify`, `entity sync --dry-run` and `entity diff` return 2 when they
find work. `joka status` never does — it is an inventory, not a gate. Both 1 and 2 are non-zero, so
a check for plain failure is unaffected.

```bash
joka entity sync --dry-run; case $? in
  0) echo "seeds are up to date" ;;
  2) echo "seeds need applying" ;;
  *) echo "joka could not tell" ;;
esac
```

## Waiting for the database

`--wait 30s` retries the connection until the deadline instead of failing on the first attempt. Use
it in a container entrypoint rather than writing a readiness loop around joka. A DSN joka cannot use
is still refused immediately.

### Share seeds between environments

`entities:` takes a list, synced as one desired state, and a reference resolves across all of it:

```yaml
entities:
  - ../shared/entities
  - ./entities
```

Put what every environment has in the shared root, and in the environment's own root put the
fixtures only it needs — plus an `overrides:` block for the handful of fields that differ:

```yaml
overrides:
  - _id: lgc_client
    redirect_uri: https://test.example.com/callback
```

An override sets column values on an entity declared elsewhere in the set, merging per column. It
cannot change `_is`, `_has`, `_pk` or `_once`: those say what an entity is, and an entity that is a
different thing per environment is two entities. An override naming an `_id` nothing declares is
refused, so a typo is not a silent no-op.

Two roots must not overlap — the same directory twice, or one inside another, is refused, because
every file in it would be declared twice.

### Reach a database in a private subnet

```yaml
connection:
  source: secret
  host: db.private.example.com
  port: 5432
  secret: { secret_id: prod/db, region: ap-northeast-1 }
  tunnel:
    target: i-0123456789abcdef0
```

joka opens the port forward, connects through it and closes it on the way out. Needs `aws` and
`session-manager-plugin` on PATH; it names whichever is missing. `remote_host` and `remote_port`
default to the connection's own, so the database is named once.

## Things that will catch you out

**`-e` does not override an exported variable.** If `DATABASE_URL` is already in
the environment, the dotenv named by `-e` will not replace it. Unset it first if
you mean the file to win.

**One entities directory per database.** Two entity sets pointed at one database
will each delete the other's rows, because the other's entities are tracked and
declared nowhere. Per-environment trees (`entities/local`, `entities/dev1`) are
selected by `--entities` or a profile, and only one is ever loaded.

**`--auto` and `--output json` need `--allow-delete` before they may delete.** The confirmation is
what gates deletion interactively, and those two skip it, so a run with nobody watching refuses
rather than removing rows a mistake left undeclared:

```
this run would delete rows and nothing said that was allowed: 2 rows (listed above).
Pass --allow-delete if that is what you meant, or run without --auto to confirm them
one plan at a time
```

A `removed:` entry is exempt — somebody wrote the `_id` down and a reviewer saw it. `joka reset` is
exempt too.

**A declined prompt exits non-zero.** `joka migrate up && joka entity sync` stops if you answer
anything but `yes` to the migration, rather than syncing against a schema that was never migrated.

**joka must be run from the directory holding its config.** It reads `.jokarc.yaml` from the working
directory and never searches upward. From anywhere else it refuses before opening a connection, so
nothing is written to whatever `DATABASE_URL` happened to point at.

**Deleting a seed file deletes its rows.** This is the point, but it surprises
people. Use `removed: … keep: true` if you meant to keep them.

**A row joka cannot match to any declaration is never deleted.** Rows written
before joka recorded `_id`s have no key, so joka reports them and leaves them
alone. `joka status` counts them.

## Writing entity files

```yaml
entities:
  - _is: users              # table (required)
    _id: admin              # identity (required, unique across the whole set)
    email: admin@example.com
    password_hash: "{{ argon2id|admin123 }}"
    _once:
      - password_hash       # seeded on insert, never written again
    _has:                   # children, inserted after the parent
      - _is: profiles
        _id: admin_profile
        user_id: "{{ admin.id }}"
```

Reserved keys: `_is`, `_id`, `_pk` (primary key column, defaults to `id`),
`_has`, `_once`.

Template expressions, resolved at insert time:

| | |
|---|---|
| `{{ now }}` | UTC timestamp |
| `{{ <id>.id }}` | the primary key of another entity |
| `{{ argon2id\|<plaintext> }}` | Argon2id hash |
| `{{ sha256\|<value> }}` | SHA-256 hex digest |
| `{{ lookup\|table,return_col,where_col=value }}` | a value from an existing row |
| `{{ asm.<source>.<key> }}` | an AWS Secrets Manager value |

**Use `_once` for anything the application owns after seeding** — a password the
user can change, a token they can rotate. Without it, every sync writes the seed
value back.

## When something refuses

| Message | What it means |
|---|---|
| `the database changed since joka last wrote` | A conflict. Decide with `--on-conflict`. |
| `this database belongs to a different joka root` | Another joka root claimed this database. Check you are in the right directory; `--adopt-root` moves the claim deliberately. |
| `not a joka directory` | You are not in the directory holding the `.jokarc.yaml`. Nothing was written. |
| `the state file describes a different database` | The identity in `joka.state.json` disagrees with the database. Either the connection is wrong, or the state file is stale — remove it or point `--statefile` elsewhere. Read-only commands still work. |
| `entity set is not valid` | An `_id` is missing, claimed twice, or contradicted by a `removed:`/`moved:` entry. It lists every problem, not just the first. |
| `tracking upgrade is blocked` | Two tracked rows claim one `_id`. No joka command can fix it — drop the losing claim from `joka_entity_rows` directly. |
| `tracked rows are still in the database` | Something is being asked to drop tracking for live rows. |
| `foreign key constraint prevented deletion` | A delete hit a foreign key from outside the seeded set. joka orders children before parents within the set but cannot order around external references. |

## Rules

- **Run `joka status` before deciding anything**, and `--dry-run` before applying.
- **Never use `drop` or `reset` on a database you do not own.** They destroy every
  table. `reset` re-seeds afterwards; `drop` does not.
- **Do not silence a conflict to make a command succeed.** It is telling you the
  database moved.
- **`migrate verify` is the CI drift gate**, not `status` — status always exits 0.
- **Migrations are forward-only.** There is no down migration; write a new one.
