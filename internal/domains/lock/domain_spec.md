# Lock Domain

Provides database-backed advisory locking to prevent concurrent mutating operations. Inspired by Terraform's state locking — only one process can run a mutating command at a time.

## Table

### `joka_lock`

Holds at most one row. The presence of a row means a lock is held.

| Column | Type | Notes |
|--------|------|-------|
| `id` | `INT PK DEFAULT 1` | Always 1 — enforces single-row constraint via primary key |
| `locked_by` | `VARCHAR(255)` | `hostname:pid` of the locking process |
| `locked_at` | `TIMESTAMP DEFAULT CURRENT_TIMESTAMP` | When the lock was acquired |
| `operation` | `VARCHAR(255)` | Which command holds the lock (e.g. `"migrate up"`, `"entity sync"`) |

The table is auto-created on first lock attempt — no `joka init` needed.

## How It Works

The gate is a PostgreSQL **session-level advisory lock**, held on a dedicated connection. The
`joka_lock` table is purely informational — who holds it, since when, for what — and is not the gate.

That split is the point. When the row *was* the gate, a run that crashed left one behind and no
later run could start until somebody cleared it by hand. An advisory lock is released by the
database when the session ends, however the process died, so a crash cannot strand anything.

### Acquire

```sql
SELECT pg_try_advisory_lock($1)          -- the gate
INSERT INTO joka_lock (id, locked_by, locked_at, operation)
  VALUES (1, $1, NOW(), $2)
  ON CONFLICT (id) DO UPDATE SET ...     -- the visibility row
```

- **Acquired** — the visibility row is upserted, so a stale row from a crashed run is overwritten
  rather than treated as a conflict.
- **Not acquired** — a live session holds it. The visibility row is read to say who and what, and
  `ErrLockHeld` carries that; if there is no row, the bare sentinel is returned.

`pg_try_advisory_lock` never waits, so contention fails fast instead of hanging.

### Release

```sql
SELECT pg_advisory_unlock($1)
DELETE FROM joka_lock WHERE id = 1
```

Nil-safe: an adapter that never acquired the lock just clears any leftover visibility row, which is
the path `joka unlock` takes to tidy up after a crashed run whose advisory lock is already gone.

### Escape Hatch

A crash no longer strands the lock itself — the database drops the advisory lock when the session
ends — but the visibility row survives, so `joka status`-style reads and the next `Acquire` would
report a holder that is gone. `joka unlock` clears it, printing who it says holds it first.

## Integration Points

The lock is acquired and released directly in the command handlers, not in the domain actions:

- `cmd/migration/up.go` — `"migrate up"`, before applying migrations.
- `cmd/entity/sync.go` — `"entity sync"`, before reconciling the entity set.
- `cmd/dbtools/drop.go` — `"drop"`.
- `cmd/dbtools/reset.go` — `"reset"`, one outer lock for the whole pipeline; the steps it calls
  take `SkipLock` so they do not try to take it again.

Each is released via `defer`.

The lock is **not** held inside the transaction — it wraps the entire command including the user confirmation prompt. This means the lock is held for the duration of the interactive session, which is intentional: we want to prevent two operators from even starting to review migrations at the same time.

## Layer Responsibilities

### `domain/`
- `Lock` — Struct representing a lock row.
- `ErrLockHeld` — Sentinel error returned when a lock cannot be acquired.

### `app/`
- `LockAdapter` — Interface abstracting lock storage.
- `AcquireLockAction` / `ReleaseLockAction` — Thin action wrappers around the adapter.

### `infra/`
- `PostgresLockAdapter` — Session advisory lock on a dedicated connection, plus the `joka_lock` visibility row.
- `lockerIdentity()` — Returns `hostname:pid` string for the current process.

## Commands

| Command | What it does |
|---------|-------------|
| `joka unlock` | Force-releases a held lock (escape hatch for crashed processes) |
