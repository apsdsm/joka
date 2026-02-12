# Lock Domain

Provides database-backed advisory locking to prevent concurrent mutating operations. Inspired by Terraform's state locking — only one process can run `migrate up` or `data sync` at a time.

## Table

### `joka_lock`

Holds at most one row. The presence of a row means a lock is held.

| Column | Type | Notes |
|--------|------|-------|
| `id` | `INT PK DEFAULT 1` | Always 1 — enforces single-row constraint via primary key |
| `locked_by` | `VARCHAR(255)` | `hostname:pid` of the locking process |
| `locked_at` | `TIMESTAMP DEFAULT CURRENT_TIMESTAMP` | When the lock was acquired |
| `operation` | `VARCHAR(255)` | Which command holds the lock (e.g. `"migrate up"`, `"data sync"`) |

The table is auto-created on first lock attempt — no `joka init` needed.

## How It Works

### Acquire

```
INSERT INTO joka_lock (id, locked_by, operation) VALUES (1, ?, ?)
```

- **Success** — Row inserted, lock acquired.
- **Duplicate key error** — Lock is held by someone else. Read the existing row and return `ErrLockHeld` with details (who, when, what operation).

This is a single atomic INSERT. No SELECT-then-INSERT race condition.

### Release

```
DELETE FROM joka_lock WHERE id = 1
```

Safe to call even if no lock is held (deleting zero rows is a no-op).

### Escape Hatch

If a process crashes without releasing the lock, the row stays behind. The `joka unlock` command force-deletes it. It prints the current holder's info before releasing so the operator knows what they're overriding.

## Integration Points

The lock is acquired and released directly in the command handlers, not in the domain actions:

- `cmd/migration/up.go` — Acquires lock with operation `"migrate up"` before applying migrations. Released via `defer`.
- `cmd/template/sync.go` — Acquires lock with operation `"data sync"` before syncing template data. Released via `defer`.

The lock is **not** held inside the transaction — it wraps the entire command including the user confirmation prompt. This means the lock is held for the duration of the interactive session, which is intentional: we want to prevent two operators from even starting to review migrations at the same time.

## Layer Responsibilities

### `domain/`
- `Lock` — Struct representing a lock row.
- `ErrLockHeld` — Sentinel error returned when a lock cannot be acquired.

### `app/`
- `LockAdapter` — Interface abstracting lock storage.
- `AcquireLockAction` / `ReleaseLockAction` — Thin action wrappers around the adapter.

### `infra/`
- `MySQLLockAdapter` — MySQL implementation using INSERT/DELETE on `joka_lock`.
- `lockerIdentity()` — Returns `hostname:pid` string for the current process.

## Commands

| Command | What it does |
|---------|-------------|
| `joka unlock` | Force-releases a held lock (escape hatch for crashed processes) |
