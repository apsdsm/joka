# Proposal: track entity rows by identity, not by position

Status: **open**, for discussion. Raised from a real failure in jjc2_main on 2026-08-26.

**Version:** joka 0.13.0 (`7d7a47e`) · **Driver:** PostgreSQL 16

## What happened

`jjc2_main` would not start. `.pairinrc.toml` chains startup as

```
joka init && joka migrate up && joka entity sync && go run ./cmd/api
```

and `entity sync` exited non-zero, so the api never ran:

```
Error: entity file changed structurally; use 'entity reimport':
04_fields/system_fields.yaml now defines 48 entities but 38 are tracked
(an entity was added or removed)
```

The change that caused it was **adding five fields to a seed file** — an ordinary edit, the most
common one anybody makes to `04_fields/system_fields.yaml`. The remedy joka names,
`entity reimport`, deletes every row the file owns and re-inserts them.

That is a large hammer for "I added a field", and the reason it is needed is a design choice worth
revisiting.

## The design choice

`sync` updates a modified file's rows **in place, by position**
(`internal/domains/entity/app/sync_entities.go:101`). The file's entity graph is flattened
depth-first, `joka_entity_rows` is sorted by `insertion_order`, and entity #N is UPDATEd onto tracked
row #N. `alignTrackedRows` (`:144`) refuses when the two lengths differ, because at that point
position no longer identifies anything.

That refusal is **correct given positional matching**. Updating row #6 with entity #6 after an
insertion at #3 would write a field's label onto a different field. The guard is not the bug.

**The bug is that position is the key at all.** Every entity in the failing file carries an `_id`:

```yaml
- _is: fields
  _id: field_company_ceo
  ...
  _has:
    - _is: field_versions
      _id: field_company_ceo_v1
```

All 48 of them, and joka **already stores it**: `TrackedRow.RefID`
(`internal/domains/entity/domain/entity.go:32`) is populated from that `_id` and persisted in
`joka_entity_rows.ref_id`. `alignTrackedRows:172` even reads it — but only as a tripwire, to detect
that positions have gone wrong. It is never used to *find* the row.

So joka holds a stable identity for every row and matches on an unstable one instead.

## What matching by `_id` would buy

| Edit | Today | By `_id` |
|---|---|---|
| Add an entity | `ErrStructuralChange` → destructive reimport | INSERT the new one, UPDATE the rest |
| Remove an entity | `ErrStructuralChange` → destructive reimport | DELETE that row (or refuse, loudly, naming it) |
| Reorder entities | `ErrStructuralChange` (the `_id` tripwire) | No-op |
| Rename a field's label | UPDATE in place | UPDATE in place |
| Move an entity between files | Orphan + duplicate | Still a problem — see below |

The first three are the whole of what people actually do to seed files, and all three are currently
one error message with one destructive answer.

## The second failure in the same class

`entity status` on the same database also reports:

```
[orphaned]  08_entity_slot_assignments/system_assignments.yaml
```

The file was renamed to `08_mappings/system_mappings.yaml`. Tracking is keyed by **path**, so the old
path is orphaned and the new path was inserted fresh. Two observations:

1. **`sync` does nothing about orphans.** There is no mention of `StatusOrphaned` in
   `sync_entities.go`; only `entity status` ever names them. The tracking rows, and whatever
   application rows they point at, stay forever.
2. **The orphan here points at a table that no longer exists.** `entity_slot_assignments` was dropped
   by a later migration, so `joka_entity_rows` holds a row referencing `to_regclass(...) IS NULL`.
   Benign until somebody runs `entity reimport` on that path, which would `DELETE FROM` a missing
   table.

Same root: **the tracking key is a location (path, position), not an identity.**

## Two smaller things noticed in the same session

**`--dry-run` cannot survey.** `joka entity sync --dry-run` aborts on the first structural error with
the same message and exit code as a real run, so it reports one bad file and nothing about the other
nineteen. A dry run's whole job is to tell you what you are in for; here it tells you the first thing
and stops. It should collect per-file outcomes and report them all.

**The update diff is unreadable for JSON columns.** A modified file's before/after prints the DB value
as Postgres renders `jsonb` (sorted-ish keys, spaces after colons) against the YAML's raw string
(compact, authored key order). A semantically identical value therefore prints as a whole-line change:

```
  structure:
    - {"levels": [{"xid": "lvl_...", "name": {"en": "Departments", "ja": "部署"}, ...
    + {"schema_version":"2","levels":[{"xid":"lvl_...","name":{"ja":"部署","en":"Departments"}, ...
```

Both sides say the same thing. Two real seed files updated in this session showed this, and neither
diff let a reader see what had actually changed. Normalising both sides through the same JSON encoder
before diffing would make the output mean something.

## Options to workshop

### A. Match by `_id`, fall back to position

Change `alignTrackedRows` to build `map[refID]TrackedRow` and align on that. Entities with no `_id`
keep positional handling within their table.

- Smallest change; no schema change (`ref_id` is already stored and already populated).
- `_id` is optional today, so the fallback keeps mixed files working — but it also means the good
  behaviour is only available to files that opted into `_id`, silently.
- Adding a row mid-file becomes an INSERT, which needs `insertion_order` to be re-numbered or made
  sparse. Worth deciding which.

### B. Make `_id` mandatory, then match on it only

Same as A, minus the fallback. `joka entity validate` (or the parser) refuses a file where any entity
lacks an `_id`.

- One rule instead of two, and the failure is at parse time rather than at the next structural edit.
- A migration burden for existing files, and a breaking change for anybody's seeds that omit `_id`.
- Arguably what `_id` was always for — it is already required for `{{ ref.id }}` templating, so most
  entities carry one anyway.

### C. Key tracking on (table, natural key) instead of `_id`

Track the row by a column the seed already declares as unique — `xid`, `name`, `code`.

- Survives a file being renamed **or split**, which neither A nor B do: the row is found wherever it
  is defined.
- Needs joka to know each table's natural key, which is per-project configuration it does not have
  today. Probably a `_key: xid` on the entity, which is `_id` with extra steps unless it is genuinely
  the DB value.

### D. Leave matching alone; make the failure non-fatal and the fix non-destructive

Keep positional matching, but have `sync` report a structural change as a **warning that skips that
file** rather than a non-zero exit, and add a `entity reconcile <file>` that diffs by `_id` and emits
the INSERT/UPDATE/DELETE plan for a human to confirm.

- Fixes the "the app will not boot because a seed grew" problem without touching the matching model.
- Leaves the file un-synced, so the next person gets a database that silently disagrees with the
  YAML — which is the failure mode `sync` exists to prevent.

### Recommendation

**A now, B when there is an appetite for a breaking change**, plus D's non-fatal exit regardless of
which matching model wins. Whatever the key is, `entity sync` bringing an application's boot down
because a seed file gained a row is worth fixing on its own.

Orphans want their own answer and are not covered by any of the above: at minimum `sync` should say
"3 tracked files no longer exist on disk" rather than leaving it to a command nobody runs.

## Reproduction

1. Any project with a synced entity file.
2. Add one entity to it — a new `- _is:` block with its `_has:` children.
3. `joka entity sync`.

```
Error: entity file changed structurally; use 'entity reimport':
<file> now defines N entities but M are tracked (an entity was added or removed)
```

Exit code is non-zero, so any `&&` chain after it does not run.

For the orphan: sync a file, `git mv` it, `joka entity status`. It is reported orphaned, and no
command other than a manual `DELETE` will ever clear it.
