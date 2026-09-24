# Proposal: consolidate entity tracking onto one reconcile path

Status: **open**, for discussion. Raised from a read of the entity domain on 2026-09-18, after the
`_id` identity work landed.

**Version:** joka 0.14.0 (`0cc5dcb`) · **Driver:** PostgreSQL 16

`proposal_entity_identity_20260826.md` proposed matching tracked rows by `_id`. That was done
(`8ffe316`, `385ec84`, `0c4e64d`). This proposal is about what the change left behind: `entity sync`
was rewritten around set-wide identity, and `reimport`, `update`, `diff` and `status` were not. There
are now two tracking models in the same domain, and the commands that use the older one can delete
rows sync would refuse to delete, fail with constraint errors sync was written to avoid, and report
verdicts that no longer exist.

Twelve findings, in four groups: two models (4), duplicated logic (3), dead and vestigial code (3),
and type limits (2). Each names the failure it produces.

## How tracking works today

Three records describe an entity, and every command is a comparison between two of them.

| Record | Where | Key | What it asserts |
|---|---|---|---|
| declared | `devops/entities/*.yaml` | `_id` | what the entity should be |
| tracked (file) | `joka_entities` | `entity_file` | the SHA-256 of that file at last sync |
| tracked (row) | `joka_entity_rows` | `ref_id`, unique since tracking v2 | which database row an `_id` became |
| live | the seeded table | primary key | what is actually there |

`entity sync` (`cmd/entity/sync.go` → `app.PlanSyncAction` → `app.ApplySetAction`):

1. Discover every `.yaml`/`.yml` under the entities dir; hash and parse each one. Unchanged files are
   parsed too, because `_id` uniqueness is a property of the set.
2. `ValidateEntitySet` over all of them: every entity has an `_id`, no `_id` is claimed twice.
3. Build `dirty` — files whose stored hash is absent, empty or different, or every tracked file under
   `--force`.
4. `PlanSyncAction` reads all of `joka_entity_rows` into `byRef` keyed on `ref_id`, walks the declared
   files in load order, and for each entity in a dirty file: absent from `byRef` → planned insert;
   present with a different table → planned refusal; otherwise `ResolveRowChanges` reads the live row
   and diffs it column by column.
5. Print the plan, confirm, open a transaction.
6. `ApplySetAction` walks the same loop again and writes: `InsertRow` + `RecordEntityRow` for new
   `_id`s, `UpdateRow` (every column) for tracked ones, `RetrackEntityRow` when the file or position
   moved, then the per-file hash record.
7. Tracked rows no file declares are reported. Nothing is deleted. `forgetEmptyFiles` drops the
   `joka_entities` record of a source file that is gone and now tracks nothing.

The other four commands do not use that path:

- **`entity reimport`** deletes every row tracked to one *file* in reverse `insertion_order`,
  re-inserts the file's graph via `InsertGraphAction`, re-records the rows.
- **`entity update`** reads that file's tracked rows, skips `_id`s already present, appends the rest
  with `insertion_order` continuing from the file's maximum.
- **`entity forget`** deletes the tracking for a file, refusing while any row is still live.
- **`entity diff`** aligns one file's declared graph against its tracked rows — by `_id` when both
  sides are fully keyed, positionally otherwise.

`internal/upgrade` holds the one versioned migration of this bookkeeping (v2: unique index on
`ref_id`), gated on blockers it checks before applying.

## Two tracking models in one domain

### 1. Identity is set-wide; change detection is per file

`ApplySetAction` skips every file not in `Dirty` (`internal/domains/entity/app/apply_set.go:102`), so
an entity is reconciled only when the file containing it was edited. A row deleted out of band is
never re-inserted: the file's hash still matches, so joka reports it synced.

`--force` was added for this case (`9664a1a`) and does not cover it. Forcing a tracked file re-enters
the plan, `ResolveRowChanges` calls `GetRow` on the missing row, and the run dies before applying
anything:

```
Error: 04_fields/system_fields.yaml: previewing fields (_id field_company_ceo):
reading fields row id=812: row not found
```

The only exit is `entity forget <file>` followed by a sync, which re-inserts the file's whole graph.

### 2. `reimport` deletes rows sync refuses to delete

`ApplySetAction` treats an undeclared tracked row as something for a human to decide about —
"a seed file edited by mistake must not take data with it". `ReimportEntityAction` deletes every row
tracked to the file (`reimport_entity.go:38`) whether or not the file still declares it, and
re-inserts only what is declared. Removing an entity from a file and running the command joka's own
output recommends therefore destroys the row, with no line naming it. The confirmation prompt says
only `Tracked rows to delete: N`.

### 3. `reimport` can hit the v2 unique index

Reimport clears tracking with `DeleteTrackedRows(file)` — one file — then `RecordEntityRow`s every
declared entity. Under the identity model an `_id` can be tracked against a different file, so this
inserts a duplicate `ref_id` and the transaction fails with a raw driver error. `RecordEntityRow`'s
own guard exists precisely because "a constraint violation is a worse way to learn that than being
told", and this path routes around it. Reimport also never calls `ValidateEntitySet`, so it can write
a set `entity sync` would refuse.

Reachable by: declare `_id x` in file B, sync (x tracked to B), move the block back to file A, run
`joka entity reimport A` instead of `joka entity sync`.

### 4. `insertion_order` has two writers, no constraint, and one command's correctness rests on it

`ApplySetAction` writes the entity's position in its file (`apply_set.go:120`). `InsertGraphAction`
writes a running counter starting at the file's current maximum for `entity update`
(`insert_graph.go:83`), and at zero for reimport. Only dirty files are re-pointed
(`apply_set.go:149`), so stale values persist.

Two rows in one file can end up sharing a position. File declares `a, b, c` at 0, 1, 2; remove `b`;
sync re-points `a`→0 and `c`→1 and leaves `b` tracked at 1 because nothing is deleted for an
undeclared entity. `entity diff` sorts on this column, `entity forget` orders its plan by it, and
`entity reimport` deletes in reverse on it for foreign-key safety.

## Logic duplicated rather than shared

### 5. Plan and apply are two copies of the same traversal

`PlanSyncAction.Execute` and `ApplySetAction.Execute` independently build `byRef`, build `refMap`,
flatten each file, test `Dirty`, branch on the table change, and collect undeclared rows. They must
agree for the preview to mean anything, and nothing but reading both enforces it.

### 6. The "modified" rule is written three times

| File | Line | Spelling |
|---|---|---|
| `internal/status/entities.go` | 87 | `dbHash == "" \|\| dbHash != hash` |
| `internal/domains/entity/app/entity_status.go` | 45 | `dbHash == "" \|\| dbHash != hash` |
| `cmd/entity/sync.go` | 154 | `!r.Force && dbHash != "" && dbHash == hash` |

Three statements of "an empty stored hash counts as modified", one of them inverted. Comments in each
place say the other two exist.

### 7. Three validators for one invariant

`app.ValidateRefIDs` (per file, duplicates only, `ErrDuplicateRefID`), `app.ValidateEntitySet`
(per set, missing and duplicate, `ErrEntitySetInvalid`), and `app.validateAllHaveRefID` (per file,
missing only, `ErrEntityMissingRefID`). `cmd/entity/update.go`'s `walkPreview` adds a fourth: a
hand-written copy of the `validateAllHaveRefID` message that does not use the sentinel, so a caller
matching on `ErrEntityMissingRefID` misses it.

## Dead and vestigial code

### 8. `entity diff` reports against a matching model sync no longer has

- `MatchByPosition` is documented as "what sync itself does" (`diff_entity.go:20`); sync has matched
  on `_id` only since `0c4e64d`.
- `PositionalBreak` is "the position sync's own matching would start writing to the wrong row"
  (`diff_entity.go:80`); no such position exists any more.
- `EntityDiff.SyncVerdict` (`diff_entity.go:89`) is **assigned nowhere**. `AlignTrackedRows` fed it
  and was deleted with `ErrStructuralChange`. The `sync_verdict` JSON key can never appear, and the
  branch at `cmd/entity/diff.go:205` — "entity sync would refuse this file", plus the
  reimport and update recommendations under it — is unreachable.

CLAUDE.md still documents the verdict line as coming from "sync's own check", and describes
positional matching as "what sync does".

### 9. `Ensure*` sniffing outlived the mechanism built to replace it

Five command files open with the same three-call preamble — `EnsureTrackingTable`,
`EnsureRowTrackingTable`, `EnsureContentHashColumn` — each wrapped in its own JSON-versus-text error
block. `EnsureContentHashColumn` queries `information_schema` to decide whether to `ALTER TABLE`,
which is the format-sniffing `internal/upgrade` and `tracking_version` exist to retire. It runs on
every entity command, including `entity status`.

### 10. Unused surface on a 22-method adapter interface

`app.DBAdapter` has 22 methods, one implementation and one hand-written mock, so every addition
touches three files. It carries `RecordEntitySynced`, superseded by `RecordEntitySyncedWithHash` and
called by nothing. `GetTrackedRowByRefID` is the inverse: implemented on the Postgres adapter, absent
from the interface, called by nothing. `GetEntityHash` exists so sync can ask per file what
`GetAllSyncedEntities` already returns for the whole set.

## Type limits

### 11. Primary keys are `int64` everywhere

`TrackedRow.RowPK`, `InsertRow`, `UpdateRow`, `GetRow`, `RowExists` and `DeleteRow` all take or return
`int64`. `pkValueFromColumns` coerces a YAML float to `int64` without complaint. A UUID or text
primary key cannot be tracked at all, and `_pk` works only when the column is literally `id` or is
supplied in the file (`postgres.go:113`).

### 12. Invariants that live in comments

`ApplySetAction` assumes the set was validated — an entity with an empty `_id` would collide in
`byRef` with legacy rows whose `ref_id` is empty — and assumes it is inside a transaction. Both are
stated in doc comments; neither is expressible in the type. `ParseEntityAction` returns `Path` set to
the filesystem path, and every caller overwrites it with the relative tracking key
(`cmd/entity/sync.go:125`, `internal/status/entities.go:104`); a caller that forgets reports absolute
paths into `ValidateEntitySet`, which keys its problem locations on that field.

## Options

### A. Port `reimport` and `update` onto `ApplySetAction`

Express both as modes of the reconcile path rather than separate traversals: reimport is
"delete and re-insert these `_id`s", update is "insert-only, skip tracked `_id`s". Both then get
set-wide validation, identity matching and the undeclared-row report for free.

- Fixes findings 2, 3 and 7, and most of 4 by leaving one writer for `insertion_order`.
- `reimport`'s per-file deletion semantics change. Deleting rows the file no longer declares becomes
  an explicit opt-in (`--prune`) rather than the default, which is a behaviour change for anyone
  relying on it to clean up.
- Largest change of the four.

### B. Make row-level state drive dirtiness, not the file hash

Keep the hash as a fast path, but let the plan re-check any file whose tracked rows are not all live,
so a row deleted out of band is re-inserted rather than crashing the preview.

- Fixes finding 1, which is the only one in this list that currently produces an unrecoverable state.
- Costs one `ExistingPKs` query per table per run. `internal/status` already does exactly this and
  groups by (table, pk column), so the query exists.
- Needs a decision on what "re-insert" means for a row something else references by id.

### C. Delete the dead paths and fold the duplicated rules

Remove `SyncVerdict` and the unreachable branch under it, remove `RecordEntitySynced`,
`GetTrackedRowByRefID` and `GetEntityHash`, move the "modified" rule into one exported function, and
move the three `Ensure*` calls into one helper the commands share (or into the upgrade steps).

- Fixes findings 6, 8, 9 and 10. No behaviour change except that `entity diff` stops promising a
  verdict it cannot produce.
- Smallest change, and the one that makes the others readable. Nothing depends on it first.

### D. Widen the primary key type

`RowPK string` on the wire, with a typed value in the adapter.

- Fixes finding 11, and is the only one of the four that needs a tracking version bump and an upgrade
  step (`joka_entity_rows.row_pk` is `BIGINT`).
- No project using joka has a non-integer seeded primary key today, so this buys nothing now. Worth
  doing only alongside the `_pk` retirement already listed as open in CLAUDE.md.

### Recommendation

**C, then B, then A. D when a project needs it.**

C first because it is small, removes the two places the code lies about itself (the dead verdict, the
stale positional comments), and shrinks what A has to move. B second because finding 1 is the only
unrecoverable state here — `--force` was added to answer it and does not. A last because it is the
real consolidation and it changes `reimport`'s contract, which wants its own discussion.

Finding 4's duplicate `insertion_order` is worth a decision independent of all four: either it is a
dependency order with one writer and a uniqueness constraint per file, or it is replaced by the
nesting depth `flattenDepths` already computes and reimport derives its deletion order from the graph.

## Reproductions

**Finding 1** — sync cannot repair a deleted row:

1. Sync an entity file.
2. `DELETE FROM <table> WHERE id = <a tracked pk>;`
3. `joka entity sync` — reports everything synced.
4. `joka entity sync --force` — fails with `reading <table> row id=<pk>: row not found`.

**Finding 2** — reimport deletes an undeclared row:

1. Sync a file with entities `a`, `b`, `c`.
2. Remove `b` from the file. `joka entity sync` reports `b` undeclared and deletes nothing.
3. `joka entity reimport <file>` — `b`'s row is gone, named nowhere in the output.

**Finding 3** — reimport hits the unique index:

1. Declare `_id x` in `b.yaml`, sync.
2. Move the block to `a.yaml`, do not sync.
3. `joka entity reimport a.yaml` — duplicate key value violates unique constraint
   `joka_entity_rows_ref_id_key`.

**Finding 4** — duplicate `insertion_order` in one file:

1. Sync a file with entities `a`, `b`, `c` (orders 0, 1, 2).
2. Remove `b`, sync. `SELECT ref_id, insertion_order FROM joka_entity_rows WHERE entity_file = '<file>'`
   returns two rows at 1.

**Finding 8** — `sync_verdict` is unreachable: `grep -rn 'SyncVerdict' --include='*.go' .` returns
the declaration, the render branch, and no assignment.
