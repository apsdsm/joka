# Plan: entity convergence with a three-way merge and a state artifact

Status: **open**, for discussion. Designed 2026-09-18, from the read recorded in
`proposal_entity_tracking_20260918.md` and the discussion in
`conversation_entity_convergence_20260918.md`.

**Version:** joka 0.14.0 (`0cc5dcb`) · **Driver:** PostgreSQL 16

Supersedes the option list at the end of `proposal_entity_tracking_20260918.md`. That document
catalogued twelve findings in the entity domain; this one says what to build instead, and why each
choice was made.

## The goal

Seed data declared in YAML is the desired state of the database. `joka entity sync` makes the
database match it. When the database disagrees with the file, joka reports the disagreement and lets
the operator decide which side is right — and when the database is right, joka updates the seed file
to match, so the declaration stays true.

Two things follow that joka does not do today:

1. **Convergence is continuous, not one-shot.** A row edited outside joka is drift, and drift is
   detected on the next sync whether or not the file changed.
2. **The seed file is writable by joka.** Resolving a conflict in the database's favour rewrites the
   YAML.

## Why the current design cannot do this

joka has no record of what it last wrote. It has the declaration (the YAML), the identity of each row
it created (`joka_entity_rows.ref_id` → primary key), and it can read the live row. With two of those
three it can see that the file and the database disagree; it cannot see **which one moved**.

`joka_entities.content_hash` is a partial substitute: a changed file hash means the declaration moved.
But it is per file, so it cannot say which column moved, and it says nothing at all when the database
moved. That is why sync's unit of work is the file rather than the row, and that single fact produces
most of the twelve findings — `--force`, `ErrStructuralChange`, `reimport`, `update` and the
unrecoverable state in finding 1 are all consequences of the hash standing in for a comparison joka
could not perform.

## Decisions

Recorded with the reason, as settled on 2026-09-18.

### D1. A three-way merge against a recorded baseline

joka records what it last applied. Every comparison is then between three points, and the common
cases need no prompt:

| declared vs baseline | live vs baseline | Meaning | Action |
|---|---|---|---|
| same | same | nothing happened | skip |
| changed | same | the file changed | push, no prompt |
| same | changed | the database changed | prompt, or a standing policy |
| changed | changed | both moved | prompt, always |

Without the baseline every difference is a conflict and joka asks about all of them on every run.
With 294 entities that is the difference between a question twice a year and an unusable command.

### D2. The baseline stores hashes, not values

Per column: `sha256(resolved value)`. The merge only needs "did this change"; both values shown in a
prompt are read live from the file and the database.

Three reasons, in order of weight:

1. **Secrets.** `resolveColumns` resolves `{{ asm.<source>.<key> }}` into the actual secret before
   inserting. `PlanSyncAction` deliberately passes no `SecretResolver` so plaintext is never
   materialized into a plan. Storing resolved values in state would undo that, and would put Secrets
   Manager plaintext into any state file that is committed or attached to a bug report. This is
   terraform's state-contains-secrets problem; hashing removes it rather than managing it.
2. **Size independence.** State size becomes ~64 bytes per column per entity — about 190KB for
   jjc2's 294 entities — and does not grow with large JSON column values.
3. Nothing in the design needs the value back.

What this gives up: joka cannot print "I last wrote X", and state cannot be used to restore data.
Neither is a goal.

### D3. The state is one JSON document, not a table of rows

joka's access pattern is already document-shaped: the reconcile reads every tracked row at the start
and writes back at the end. The per-file queries (`GetTrackedRows(file)`, `DeleteTrackedRows(file)`)
exist only for `reimport`, `forget` and `diff`, all of which are file-scoped for a reason this plan
removes.

What a document buys:

- **Uniqueness becomes structural.** `{"entities": {"<_id>": …}}` cannot hold a duplicate `_id`. That
  is tracking version 2's unique index, enforced by the data model instead of by a constraint plus an
  upgrade step with blockers.
- **One serialization for every home.** The same JSON in a jsonb column, a file, or S3.
- **Versioning moves into the document**, so a state shape change is a field rather than a
  tracking-table migration.

What it gives up: incrementality. The row-per-entity table persisted each row as it was inserted, so
a crash mid-run left partial but accurate state. One document written at the end is all-or-nothing,
which is why D4 matters.

Concurrency is unaffected — `joka_lock` already serializes every mutating command.

### D4. The authoritative write is inside the transaction; the file is materialized after

```
BEGIN
  insert / update the seeded rows
  write the state document to joka_state          -- same transaction
COMMIT
materialize the state file
delete the joka_state row                          -- expunge
```

Ordering matters and this is the only ordering with no gap:

| Ordering | Crash window | Result | Detectable |
|---|---|---|---|
| commit → write file | after commit | rows exist, state does not | no — the next run re-inserts and duplicates |
| write file → commit | after file write | state exists, rows do not | yes, by checking the recorded PKs |
| **state in the transaction → materialize** | any | rows and state are always consistent | n/a — no window |

`PREPARE TRANSACTION` would also close it, and is rejected: `max_prepared_transactions` defaults to
`0` and needs a server restart, and a prepared transaction left dangling holds locks and blocks
vacuum indefinitely. That is a bad trade for a seed tool.

### D5. `joka_state` is a journal, not a store — it is expunged after materialization

Keeping both copies means deciding which is authoritative on every load. Expunging removes the
question: no journal row means the file is current; a journal row present means the last run
committed but did not materialize, so rewrite the file from it and expunge. Re-materializing is
idempotent, so dying between materialize and expunge is harmless.

### D6. The file is the audit record, and the database keeps only a version marker

The reason for an external copy is not durability. It is that state living inside the database it
describes is always self-consistent, so it can never report that the database is the wrong one. Three
cases only an external copy catches:

- **A restore.** A dump from before the last sync rolls the state back with the rows, so joka reads a
  state that agrees perfectly with a database that has silently lost seeds.
- **A substitution.** `DATABASE_URL` points at a stale container, a colleague's database, or staging.
  The state there describes that database correctly, so nothing objects.
- **`joka drop`.** The record of what was there goes with the thing it recorded.

For the comparison to have two sides, the database must still report its own version after the
journal is expunged. Two keys in `joka_meta`, which is not a second copy of the state:

| Key | Value |
|---|---|
| `state_identity` | UUID stamped on first write; travels with a dump |
| `state_version` | incremented in the same transaction as the rows |

| File vs database | Meaning |
|---|---|
| same identity, same version | normal |
| same identity, database behind | the database was restored or rolled back |
| same identity, file behind | the file was not materialized after the last run |
| different identity | this is not the database the file describes |

Binding is on `state_identity`, **not on the profile name**. A profile is a label someone can point
at two databases in the same week.

### D7. State has exactly one home per database

Expunging the journal makes the state single-copy, so the file's home must be unambiguous:

| Environment | Shape | Home |
|---|---|---|
| `local` | one database per developer | that developer's machine, not committed |
| `dev1`, `e2e` | one shared database | committed to git, or S3 |

This is why terraform grew remote backends, and it is the same answer. A stale file for a shared
database is detectable by D6 but **not recoverable**, because the journal it would need was expunged.
Sharing the home is what prevents that.

### D8. Losing the file is a refusal, not 294 duplicate inserts

Single-copy state inherits terraform's worst failure: lose the state and every entity reads as new.
The `state_version` marker in `joka_meta` softens it — file missing but `state_version` is 17 means
"this database has been synced 17 times and you have no record of it", which joka refuses on rather
than re-inserting. Full recovery needs adoption (D12).

### D9. Prompting is an opt-in mode; `sync` stays non-interactive

`entity sync` runs in `&&` boot chains and under `--output json`. A prompt cannot block boot.

- `joka entity sync --on-conflict=fail` — default. Reports conflicts, exits non-zero, writes nothing.
  This is the CI drift gate, the same role `migrate verify` plays for schema.
- `--on-conflict=file` — the declaration wins; push over the database.
- `--on-conflict=db` — the database wins; record the new baseline, leave the row alone, do not
  rewrite the YAML.
- `joka entity resolve` — interactive. Walks the conflicts, writes back to YAML, then applies.

Conflicts are batched, not asked one at a time: `12 columns differ across 4 rows: [f]ile wins all /
[d]atabase wins all / [r]eview each`.

### D10. The plan is a value the applier consumes

Once an operator has answered questions about a plan, the apply must execute *that* plan rather than
recomputing it. This removes finding 5 by necessity — `PlanSyncAction` and `ApplySetAction` stop
being two traversals that have to agree.

### D11. Per-column ownership, so the password case is not a conflict forever

A seed row has three kinds of column: joka owns it, joka seeds it once and then lets go, and joka
never touches it (not declared). The middle kind has no expression today, which is why a user
resetting their password would prompt on every sync — the exact problem that made entity sync
skip-once in the first place.

```yaml
- _is: users
  _id: admin
  email: admin@example.com                     # joka owns it; drift is a conflict
  _once:
    password_hash: "{{ argon2id|admin123 }}"   # set on insert, the database owns it after
```

Syntax to be decided; the distinction is not optional. Without it, `--on-conflict=fail` fails every
CI run after the first password reset.

This also closes a current blind spot: non-deterministic columns are excluded from comparison today
(`isNonDeterministicTemplate` → "regenerated"), so drift in them is invisible. A hashed baseline makes
`password_hash` comparable for the first time.

### D12. Adoption is a dependency, and it is the `_key` that was just dropped

"joka ensures the database matches the YAML" requires finding a row joka did not insert — seeded by a
migration, or left by an earlier bug. Today joka can only find rows through its own tracking, so it
inserts a second copy. Adoption needs a natural key per table, which is `_key`, removed in `0cc5dcb`
for being inert. CLAUDE.md already names the right basis: 17 of jjc2's 18 seeded tables declare
`UNIQUE (xid)`.

Not built in this plan. Recorded because D8's recovery path and full convergence both depend on it.

### D13. Write-back must be surgical, and cannot touch templated columns

`ParseEntityAction` unmarshals into `[]map[string]any`, which discards comments, key order, quoting
and anchors. A decode/encode round-trip reformats every seed file on the first conflict.

`yaml.v3`'s `yaml.Node` preserves all of it. Write-back means locating the mapping node for
`_id: <x>`, locating its key, replacing that one value node, and re-encoding. The parser has to keep
the node tree alongside the domain entity.

**A templated value cannot be pulled back.** `{{ lookup|industry_types,id,code=RESTAURANT }}` resolved
to `7` and the database now says `9`; writing `9` into the file replaces an expression with a literal
and silently breaks the indirection. Same for `{{ ref.id }}`, `{{ now }}`, `{{ argon2id|… }}` and
`asm.*`. For those columns, "the database is correct" means "record the new baseline and stop pushing
this column", which is a third answer the prompt must offer.

## The state artifact

```go
// State is what joka last applied to one database.
type State struct {
    Version  int                    `json:"version"`
    Identity string                 `json:"identity"`  // matches joka_meta.state_identity
    Sequence int                    `json:"sequence"`  // matches joka_meta.state_version
    Entities map[string]EntityState `json:"entities"`  // keyed by _id
}

type EntityState struct {
    Table    string            `json:"table"`
    PKColumn string            `json:"pk_column"`
    PKValue  int64             `json:"pk_value"`
    File     string            `json:"file"`      // where it was last declared; metadata only
    Columns  map[string]string `json:"columns"`   // column → sha256 of the applied value
}

type Backend interface {
    Load(ctx context.Context) (*State, error)
    Save(ctx context.Context, s *State) error
}
```

`joka_entities` and `joka_entity_rows` become one implementation of `Backend`. Most of the 22-method
`DBAdapter` is state access and stops being the database's business, which resolves finding 10. Tests
construct a `State` literally instead of standing up a mock adapter.

`File` is retained as metadata — it is what `entity diff <file>` and error messages need to say where
an entity came from. Nothing keys on it.

## Phases

Each phase is shippable and leaves joka working.

### Phase 0 — remove what is dead

No behaviour change. Shrinks what the later phases have to move.

- Delete `EntityDiff.SyncVerdict` and the unreachable render branch at `cmd/entity/diff.go:205`.
- Delete `RecordEntitySynced`, `GetTrackedRowByRefID`, `GetEntityHash`.
- Fold the "modified" rule into one exported function (currently `internal/status/entities.go:87`,
  `entity_status.go:45`, `cmd/entity/sync.go:154`).
- Fold the three `Ensure*` calls into one helper the five commands share.
- Correct the stale comments that describe positional matching as "what sync does".
- Update CLAUDE.md, which documents `SyncVerdict` and `AlignTrackedRows` as live.

Closes findings 5 (partly), 6, 8, 9, 10.

### Phase 1 — introduce the state artifact

No behaviour change. `State` and `Backend` as above, with a `Backend` implementation reading and
writing the existing `joka_entities` / `joka_entity_rows` tables. Every command loads a `State`
instead of querying rows.

### Phase 2 — record the baseline

Tracking version 3. `joka_state` created as a jsonb document table; the row-based tables are read for
migration and then dropped by an `internal/upgrade` step. Sync records per-column hashes. Still no
behaviour change — the baseline is written and not yet read.

Upgrade blockers: none. A state migrated from the old tables has no column hashes, which reads as
"unknown" and behaves exactly as today until Phase 3.

### Phase 3 — three-way merge, non-interactive

The first behaviour change, and the one that fixes finding 1.

- The content hash stops being the gate. Every declared entity is compared, whether or not its file
  changed.
- `--on-conflict=fail|file|db`, defaulting to `fail`.
- `_once:` columns (D11) land here, or `--on-conflict=fail` breaks every pipeline with a password.
- `--force` is removed; it existed because the hash was the gate.
- `reimport` becomes `--on-conflict=file --prune` and `update` becomes insert-only on the same path.
  Both stop being separate traversals, which closes findings 2, 3, 4 and 7.

### Phase 4 — the file, the journal and the audit

- `joka_state` writes inside the transaction; the file is materialized after commit; the journal row
  is expunged (D4, D5).
- `state_identity` and `state_version` in `joka_meta` (D6).
- Backend home configured per profile, bound to identity (D7).
- Missing file with a non-zero `state_version` is a refusal (D8).

### Phase 5 — interactive resolution and write-back

- `joka entity resolve`, batched prompts (D9).
- `yaml.Node`-preserving write-back (D13), with the third answer for templated columns.

### Deferred — adoption

`_key` and matching on a natural key (D12). Needed for full convergence and for recovering from a
lost state file. Not scheduled.

## What this does to the twelve findings

| Finding | Resolved by |
|---|---|
| 1 — hash gates reconciliation | Phase 3 |
| 2 — reimport deletes undeclared rows | Phase 3 |
| 3 — reimport hits the unique index | Phase 3 |
| 4 — `insertion_order` has two writers | Phase 3 — one writer; order survives only for FK-safe deletes |
| 5 — plan/apply duplication | Phase 0 partly, Phase 3 fully (D10) |
| 6 — "modified" rule in three places | Phase 0 |
| 7 — three validators | Phase 3 |
| 8 — dead `SyncVerdict` | Phase 0 |
| 9 — `Ensure*` sniffing | Phase 0 |
| 10 — 22-method adapter | Phase 1 |
| 11 — `int64` primary keys | unchanged; `_pk` retirement and D12 should be taken together |
| 12 — invariants in comments | Phase 1 — `State` makes them expressible |

## Open questions

1. **`_once:` syntax.** A nested block as sketched, a sibling list (`_once: [password_hash]`), or a
   per-column marker. Affects the parser and the write-back node lookup.
2. **Does the backend cover migrations?** `joka_migrations` is state too. If `.jokarc.yaml` says state
   lives in S3 and only half of it does, the config is lying. Covering migrations means the backend
   must be readable before the database is reachable, which is a different constraint set. Current
   answer: entity state only, and say so.
3. **Undeclared rows under "the YAML is desired state".** Strictly they are drift and should be
   deleted. Proposed: report always, delete only under `--prune`, offer it in `resolve`. Confirm.
4. **Natural-key primary keys and the crash check.** D4 removes the need for the recorded-PK check,
   but if a file backend is ever made authoritative, that check does not hold for
   `pkValueFromColumns` rows, where the key comes from the YAML and can be reassigned.
5. **A shared state file's own merge conflicts.** For `dev1` committed to git, two people syncing from
   different branches produce competing state files. The `sequence` field detects it; nothing resolves
   it yet.
