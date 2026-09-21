# Conversation record: entity tracking review and convergence design

Date: 2026-09-18 · joka 0.14.0 (`0cc5dcb`) · branch `visual-diagnostics`

Raw record of the discussion that produced `proposal_entity_tracking_20260918.md` (the read of the
current code) and `proposal_entity_convergence_20260918.md` (the plan). Kept because several
decisions were reached by argument rather than by reading code, and the reasoning is not recoverable
from either document alone. Two claims made in this conversation were later corrected by the other
party; both corrections are left in place rather than edited out.

---

## 1. Nick

> the current code for tracking entity imports and state is a little error prone, and feels a little
> over complicated. explain how it works now, and tell me the parts that are brittle, or feel like
> they were evolved over time rather than planned.

## 2. Claude

### How it works now

Three records describe an entity, and every command is some comparison between two of them.

| Record | Where | Key | What it says |
|---|---|---|---|
| declared | `devops/entities/*.yaml` | `_id` | what the entity should be |
| tracked (file) | `joka_entities` | `entity_file` | the SHA-256 of the file at last sync |
| tracked (row) | `joka_entity_rows` | `ref_id` (unique since tracking v2) | which database row an `_id` became |
| live | the seeded table | primary key | what is actually there |

`entity sync` (`cmd/entity/sync.go`, then `app.PlanSyncAction` and `app.ApplySetAction`) runs:

1. Discover every `.yaml`/`.yml` under the entities dir, hash each one, parse each one. Unchanged
   files are parsed too, because `_id` uniqueness is a set property.
2. `ValidateEntitySet` over the whole set: every entity has an `_id`, no `_id` twice.
3. Build `dirty` — files whose stored hash is absent, empty, or different (or every tracked file
   under `--force`).
4. `PlanSyncAction` reads all of `joka_entity_rows` into `byRef` keyed on `ref_id`, walks the
   declared files in load order, and for each entity in a dirty file: not in `byRef` → planned
   insert; in `byRef` with a different table → planned refusal; otherwise `ResolveRowChanges` reads
   the live row and diffs it column by column.
5. Print the plan, confirm, open a transaction.
6. `ApplySetAction` walks the same loop again and writes: `InsertRow` + `RecordEntityRow` for new
   `_id`s, `UpdateRow` (all columns) for tracked ones, `RetrackEntityRow` when the file or position
   moved, then `UpdateEntitySynced`/`RecordEntitySyncedWithHash` per file.
7. Tracked rows whose `ref_id` no file declared are reported as undeclared. Nothing is deleted.
   `forgetEmptyFiles` drops the `joka_entities` record of a source file that is gone and now tracks
   nothing.

The other four commands do not go through that path. `entity reimport` deletes every row tracked to
one *file* in reverse `insertion_order`, re-inserts the file's graph, re-records the rows.
`entity update` reads that file's tracked rows, skips `_id`s already present, appends the rest with
`insertion_order` continuing from the file's maximum. `entity forget` deletes the tracking for a file
and refuses if any row is still live. `entity diff` aligns one file's declared graph against its
tracked rows, by `_id` if both sides are fully keyed and positionally otherwise.

`internal/upgrade` holds the one versioned migration of this bookkeeping (v2: unique index on
`ref_id`), gated on blockers it checks first.

### What is brittle

**Identity is set-wide, but the change detector is per file.** `ApplySetAction` skips every non-dirty
file (`apply_set.go:102`), so an entity is only reconciled when the file containing it was edited. A
row deleted out of band is not re-inserted; the file's hash still matches, so joka considers it
synced. This is the state `--force` was added for, and `--force` does not work either: forcing a
tracked file re-enters the plan, `ResolveRowChanges` calls `GetRow` on the missing row, and the whole
sync dies with `reading users row id=5: row not found` before anything is applied. The only exit is
`entity forget` then sync.

**`entity reimport` and `entity update` were never ported to the identity model.** They still key on
`entity_file` and per-file position, and they skip `ValidateEntitySet` entirely.

- Reimport deletes every row tracked to the file, including rows the file no longer declares — the
  exact deletion `ApplySetAction` refuses to do on principle, performed silently by the command the
  docs recommend next to it.
- Reimport re-records rows with `RecordEntityRow`; if any of those `_id`s is tracked against a
  different file (which the identity model now makes routine), the v2 unique index fires and the user
  gets a raw `pq` constraint error. `RecordEntityRow`'s own guard was written to avoid precisely that
  class of message.
- `entity update` re-implements the "every entity needs an `_id`" check three times over:
  `app.validateAllHaveRefID` (returns `domain.ErrEntityMissingRefID`), `cmd/entity/update.go:116`'s
  `ValidateRefIDs`, and a hand-written copy of the message string in `walkPreview` that does not use
  the sentinel.

**`insertion_order` has two writers and no constraint.** `ApplySetAction` writes the entity's position
in the file (`apply_set.go:120`), `InsertGraphAction` writes a running counter from the file's current
maximum (`insert_graph.go:83`), and reimport starts it at zero. Nothing enforces uniqueness within a
file, and only dirty files get re-pointed. Remove one entity from a three-entity file: sync re-points
the survivors to 0 and 1, the undeclared row keeps 1, and the file now has two rows at position 1.
`entity diff` sorts on it, `entity forget` displays on it, and `entity reimport` deletes in reverse on
it for FK safety.

**`entity diff` reports against a matching algorithm sync no longer has.** `MatchByPosition` is
documented as "what sync itself does" (`diff_entity.go:20`), `PositionalBreak` as "the position sync's
own matching would start writing to the wrong row" — both were true before commit `0c4e64d`.
`EntityDiff.SyncVerdict` is declared at `diff_entity.go:89` and assigned nowhere: `AlignTrackedRows`
fed it and was deleted. The whole branch at `cmd/entity/diff.go:205` — "entity sync would refuse this
file", plus the reimport and update recommendations under it — is unreachable, and `sync_verdict` is
a JSON key that can never appear. CLAUDE.md still documents it as live.

**The "modified" rule is copied into three places.** `cmd/entity/sync.go:154`, `entity_status.go:45`
and `internal/status/entities.go:87` each spell out "empty stored hash counts as modified"
independently. Two of them are inverted relative to the third.

**`Ensure*` sniffing survived the thing built to replace it.** Five command files open with the same
three-call preamble, each with its own JSON-vs-text error block. `EnsureContentHashColumn` queries
`information_schema` to decide whether to `ALTER TABLE` — the format-sniffing that `internal/upgrade`
and `tracking_version` exist to retire, still running on every entity command.

**The adapter interface is 22 methods wide with one implementation and one hand-written mock**, and it
carries methods nothing calls (`RecordEntitySynced`). `GetTrackedRowByRefID` is the reverse:
implemented on the Postgres adapter, absent from the interface, called by nobody. `GetEntityHash`
exists only so sync can ask per file what `GetAllSyncedEntities` already returns for all of them.

**Plan and apply are two copies of the same traversal.** `PlanSyncAction.Execute` and
`ApplySetAction.Execute` independently build `byRef`, build `refMap`, flatten each file, test `Dirty`,
branch on the table change, and collect undeclared rows.

**Primary keys are `int64` everywhere.** `pkValueFromColumns` coerces a YAML float to `int64` without
complaint. A UUID or text primary key cannot be tracked at all.

**`ApplySetAction` depends on invariants its caller enforces** — that the set was validated, and that
it is inside a transaction. Both are stated in doc comments; neither is expressible in the type.

**`ParseEntityAction` returns `Path` set to the full filesystem path**, and every caller overwrites it
with the relative tracking key.

### What this looks like as a plan rather than an accretion

One `Reconcile` action over (declared set, tracked rows, live rows) that returns a plan, and one
applier that executes exactly that plan. `reimport`, `update` and `force` become flags on it.
`entity_file` becomes what its comment already says it is, metadata, and stops being a deletion key.
`insertion_order` becomes a dependency order for deletes with a single writer, or is replaced by the
graph nesting `flattenDepths` already computes.

---

## 3. Nick

> write this up in a proposal doc

## 4. Claude

Written to `proposal_entity_tracking_20260918.md`, matching the format of
`proposal_entity_identity_20260826.md`. Twelve findings in four groups: two tracking models in one
domain (4), duplicated logic (3), dead and vestigial code (3), type limits (2). Options A–D with a
recommendation of C (delete dead paths, fold duplicated rules), then B (row-level dirtiness), then A
(port `reimport`/`update` onto `ApplySetAction`), with D (widen the PK type) deferred.

Two things flagged: finding 8 means CLAUDE.md is currently wrong in two places, and option A changes
`reimport`'s contract, which is why the proposal puts it last.

---

## 5. Nick

> my take on this is that currently entity tracking in joka is more or less working, but there are too
> many bandaids. Let me reiterate the goals: I can define the "seed data" that I want in the data base
> in a neutral format (yaml) and joka looks at that and ensures that the db matches that state. 1st
> problem arose - what if the seed data was changed? e.g., a user reset their password or a lookup
> table changed? every seed would destroy that! second problem arrose - how do we track what is in and
> what is not? how do we track if we add a field to a row? so we start adding tracking at a table
> level. Am I correct so far?

## 6. Claude

Mostly right, with two corrections.

### What the history says

| When | Commit | What was added |
|---|---|---|
| 2026-02-24 | `a51fafa` | `joka_entities` — file path → "synced". Sync inserts a file's graph once and **skips it forever after** |
| 2026-03-06 | `fc31861` | `joka_entity_rows` (keyed on `entity_file` + `insertion_order`), `content_hash`, and `ref_id` stored but unused |
| 2026-03-10 | `6c6b70c` | `reimport` and `update` |
| 2026-06-01 | `17ebde1` | modified files updated in place, positionally, with `ErrStructuralChange` when the counts differ |
| 2026-08-27 | `0c4e64d` | matching moved to `_id` |

**Correction 1: tracking was never at table level.** It went file → row, and the row tracking was
subordinate to the file — `joka_entity_rows.entity_file` was the key, position within the file was the
identity. That is why `reimport`, `forget` and `diff` all still take a *file* argument.

**Correction 2: the original sync was not destructive, because it never re-ran.**
`SyncEntitiesAction` skipped any file already in `joka_entities`. "Every seed would destroy that" was
never a bug that happened; it was designed around, by refusing to converge at all. The cost showed up
as the opposite complaint: a changed seed file did nothing until someone ran `reimport`.

### The conflict underneath

The two goals are in direct opposition and joka has never picked between them:

- "joka ensures the db matches the yaml" — declared state wins.
- "a user reset their password, don't destroy that" — live state wins.

There is no rule that satisfies both. What exists instead is six answers, one per code path:

| Path | Who wins |
|---|---|
| `data sync`, truncate strategy | declared, always |
| `entity sync`, unchanged file | live (skipped) |
| `entity sync`, changed file, matched row | declared, every column rewritten |
| `entity sync`, undeclared tracked row | live (nothing deleted) |
| `entity reimport` | declared, destructively |
| `entity update` | live (insert-only) |

Every bandaid sits on top of that unmade decision. `--force` exists because rule 2 is wrong sometimes.
`ErrStructuralChange` existed because rule 3 needed matched rows. `reimport` exists because rule 2
left no way to re-apply. `update` exists because `reimport` was too destructive. `forget` exists
because rules 4 and 6 leave tracking nothing else clears.

### The content hash is the bandaid, not the tracking

The hash answers "did the author's intent change?" It was needed because joka had no way to ask the
question it actually cares about: does the database match the declaration?

It can ask that now. `ResolveRowChanges` takes a declared entity and a live row and returns the
columns that differ, semantically, with regenerated values excluded. That is a convergence check,
currently gated behind the hash. If the hash became a fast path rather than the gate, most of the list
collapses, and what is left to decide is the one real question: for a column that differs, does the
file win or does the database?

---

## 7. Nick

> The intent that I think we should pursue: joka will push data into the database. the seed data where
> joka is run is assumed to be the *desired* content. IF the database content does not match (a field
> was changed from a to b) then joka will pause and ask the user "which is correct, a or b?" if the
> user says b, joka updates the seed file to match b, then proceeds. This means that joka kind of has
> a terraform state, that needs to be checked in, but unlike terraform it can also self heal.

## 8. Claude

The shape is right. Three things it needs that joka does not have, one of which is the whole design.

### The design needs a third point, not two

"If the database does not match, ask" only works if joka can answer which side moved. `a` in the file
and `b` in the database is three different situations: the file changed, the database changed, or
both. Terraform distinguishes these with state. joka's version belongs in `joka_entity_rows` — the
resolved value (or its hash) per column, as last applied.

| declared vs baseline | live vs baseline | Meaning | Action |
|---|---|---|---|
| same | same | nothing happened | skip |
| changed | same | the file changed | push, no prompt |
| same | changed | the database changed | prompt, or a standing policy |
| changed | changed | both moved | prompt, always |

Without it every difference is a conflict and joka asks about all of them on every run. With 294
entities that is the difference between a question twice a year and an unusable command.

joka half has this already. `joka_entities.content_hash` is "the declaration as last applied" — but at
file granularity, so it can say the file changed and not which column.

### Write-back is the hard part, and it constrains the parser

`ParseEntityAction` unmarshals into `[]map[string]any`, which throws away comments, key order, quoting
and anchors. `yaml.v3`'s `yaml.Node` keeps them, so write-back has to keep the node tree alongside the
domain entity.

**Templated values cannot be pulled back at all.** `{{ lookup|… }}` resolved to `7` and the database
says `9`; writing `9` into the file replaces an expression with a literal and silently breaks the
indirection. Same for `{{ ref.id }}`, `{{ now }}`, `{{ argon2id|… }}` and `asm.*`. "The database is
correct" has to mean "record the new baseline and stop pushing this column", a different answer that
needs its own word in the prompt.

### The password case wants per-column ownership, not a prompt

A user resetting their password is not a conflict to resolve once; it is a column the application owns
permanently. Under this design it prompts on every sync forever unless the file can say so
(`_once:` or similar). Seed rows have three kinds of column: joka owns it, joka seeds it and then lets
go, and joka never touches it.

This also fixes a blind spot — non-deterministic columns are excluded from comparison entirely today,
so drift in them is invisible. A baseline makes `password_hash` comparable for the first time.

### Prompting has to be optional, and the answers have to bind the apply

`entity sync` runs in `&&` boot chains and under `--output json`. Shape that fits: `entity sync`
non-interactive with `--on-conflict=fail|file|db`, and `entity resolve` interactive. Conflicts
batched, not asked one at a time.

One consequence: once the user has answered questions about a plan, the apply must execute that plan
rather than recomputing it. That kills finding 5 by necessity.

### What it will force back that was just dropped

Converging properly needs adoption, and adoption needs a natural key per table. That is `_key`,
dropped in `0cc5dcb`. CLAUDE.md already notes the right basis: 17 of jjc2's 18 seeded tables declare
`UNIQUE (xid)`.

### One correction to the framing

There are two artifacts and only one gets checked in: the YAML (desired state, in git) and the
baseline (what joka last applied, in the database, never in git).

---

## 9. Nick

> so what if we make a joka state file like terraform has? does that solve the problem? might not even
> need anything on the db in that case...

## 10. Claude

A state file solves the merge — any baseline does — but it costs a subsystem where the database costs
one column.

Objections raised: it cannot be checked in because what it holds is per-database; it cannot be written
in the transaction; `joka drop`/`reset` would leave it stale; one checkout already syncs several
databases. Plus: `joka_lock` and `joka_migrations` have to stay database-resident anyway, so a state
file removes two of five `joka_*` tables and splits the truth across two stores.

Proposed instead: `ALTER TABLE joka_entity_rows ADD COLUMN applied_values JSONB`, a tracking version
bump to 3 with an empty blocker list.

> **This message contained an error, corrected in §12.** The claim that terraform state is
> "deliberately not committed" was an overstatement of HashiCorp's guidance presented as universal
> practice.

---

## 11. Nick

> first, you DO check in Terraform state though? it's commonly done specifically BECAUSE it's per env?
> our terraform envs are stored in git OR they are stored in S3. Different envs have different stte
> files. Next, what I hear you saying is that you would store what basically is a state file on the db?

## 12. Claude

Conceded. Committing state per environment is a real arrangement, it works, and "git or S3 depending
on the env" is normal. HashiCorp's guidance against committing is about secrets in the state and the
absence of locking, not about it being unworkable. "Not committed by default" would have been
accurate; "deliberately not committed" was not.

The git-conflict objection is wrong as stated for `dev1` and `e2e` — one shared database each, applied
by one person or CI at a time, exactly like terraform.

It holds for `local`, and that is where joka differs: `local` is not one environment, it is N
databases, one postgres per developer, all reading the same `local/` seed tree from the same checkout.
A committed `state/local.json` holds one developer's primary keys and is wrong for everyone else.
Terraform rarely has this because nobody runs a personal copy of `dev1`.

**And yes — a state file stored in the database is exactly the proposal.** joka already has one and has
never called it that. `joka_entities` + `joka_entity_rows` records, per managed object, which real
resource it became. What it is missing is the attributes. So the question is not "file or database",
it is which backend:

| Backend | Locking | Atomic with the write | Correct after `joka drop` | Per-developer local DB |
|---|---|---|---|---|
| git | none | no | no | broken |
| S3 | needs a lock object | no | no | broken |
| the target database | already there (`joka_lock`) | yes | yes | works |

The one real argument for an external backend is schema cleanliness, which would need `joka_migrations`
and `joka_lock` to move too, and `joka_lock` cannot leave without losing the property that makes it a
lock.

---

## 13. Nick

> to me that sounds like "it doesn't actually matter where the state file lives so long as you know
> which state file belongs to which db" and that is doable in config like Terraform does. So some teams
> could have the state file as a file, or keep it a file for local, or others could use S3 or whatever.

## 14. Claude

Agreed on the binding, with one caveat: bind to **database identity, not profile name**. A profile is
a label someone can point at two databases in the same week; the DSN or a UUID in `joka_meta` is what
actually identifies whose state this is.

**What doesn't go away: inserts.** An external backend cannot write state in the same transaction as
the rows. For updates that is harmless. For inserts it is not, and it cannot be journaled away,
because the primary keys come back from `RETURNING` — they do not exist until the insert has run.
Recovery afterwards needs a natural key, which is the `_key` hole again.

**What I was overweighting: drop and restore.** `joka drop` can delete the state object if it knows
where it is, and a restored database is the same situation as infrastructure deleted in a console;
terraform answers that with `refresh` and joka needs the equivalent check anyway (finding 1).

**What the framing is worth:** making the location configurable forces the state to become a defined
artifact — a value with a schema, a serialization and a version — instead of three tables with
semantics spread across six commands. Worth doing whether or not a second backend ships.

**One scoping question:** `joka_migrations` is state too. If `.jokarc.yaml` says state lives in S3 and
only half of it does, the config is lying.

**Where I'd land:** define `State` and `Backend` now, ship the database backend as the default and the
only implementation, and document that it is the only one transactional with the writes it describes.

---

## 15. Nick

> "2. Process dies before the state file is written." <-- just stopping on this. so your argment is
> that the process dies before the state file is written, and you lose info, but if you do it as one
> transaction the worst that can happen is the transaction is not commited. correct?

## 16. Claude

Correct, and the property is slightly stronger than "you lose info versus you don't":

- **DB-resident state, one transaction.** No moment exists where rows exist without state or state
  exists without rows. Death before commit rolls back everything; death after commit leaves both
  durable. Every outcome is a state joka can plan from.
- **External state.** Commit then write: rows exist, nothing records them, next run inserts them
  again.

> **Correction to §14.** The claim that the insert gap cannot be closed for an external backend was
> wrong. It can, by writing and fsyncing the state **before** `COMMIT`. The failure then becomes state
> referencing rolled-back rows, which is recoverable: a load-time check that each recorded primary key
> still exists demotes those entries to not-applied. Postgres does not return sequence values on
> rollback, so a dangling key from a rolled-back insert is never reused by a different row.

Two caveats on that fix: it does not hold for the natural-key path (`pkValueFromColumns`), where the
key comes from the YAML; and it requires the write ordering to be right in every code path forever.
The database backend has neither requirement because the guarantee comes from the transaction rather
than from discipline.

---

## 17. Nick

> two things - first I want to move from hundreds of rows for statemanagement to a single json file
> stored as jsonb in the db for management - and the other is that i think you'd have to download that
> state to local before you started working on the db anyway, so there is kind of a little bit of the
> storage location feeling off for me. The other thing is how likely is it that the process just dies?
> I mean, it hasn't happened to me once using joka, but I'm not saying it's impossible either. how does
> terraform manage that (I guess the state is written as the job proceeds?)

## 18. Claude

### A single jsonb document is the better shape

joka's access pattern is already document-shaped. The per-file queries exist only for `reimport`,
`forget` and `diff`, all file-scoped for the historical reason already identified as wrong.

Gains: uniqueness becomes structural (an object cannot hold a duplicate key, which is tracking version
2's unique index enforced by the data model); the state serializes identically wherever it lands, so
the backend interface becomes `Load() []byte` / `Save([]byte)`; versioning moves into the document.

Not lost: concurrency is already serialized by `joka_lock`; size is a few hundred KB for 294 entities.

Lost: incrementality, which matters for the next two answers.

### You are right that location matters less than I implied

The run is always load → reconcile in memory → write back. The state is checked out and checked in,
not queried live. Once that is true and the write is a single document, a file written to a temp path
and renamed is atomic too. What is left is exactly one property: the check-in can be in the same
transaction as the data writes. Not four advantages, one.

### Process death is rare, and the likely cause is the benign one

| Cause | Frequency | Outcome |
|---|---|---|
| Ctrl-C at the prompt or during apply | common | transaction rolls back — safe either way |
| DB connection drops mid-transaction | occasional | server rolls back — safe |
| CI container OOM-killed during `joka reset` | rare | the dangerous one |
| Machine crash | rare | the dangerous one |

**Terraform's answer is incremental writes plus a lock.** State is persisted as each resource is
created; the remote backend holds a lock for the run; anything still outside state is adopted with
`terraform import`. joka has the lock and has no import, which is the `_key` hole again.

Consequence: **a single document is less incremental than the row-per-entity table was.** Today each
`RecordEntityRow` lands as it goes. One document written at the end is all-or-nothing, so the crash
window covers the whole run — which is why atomicity has to take over the job incrementality was
doing.

---

## 19. Nick

> so, basically as you write seeds to the bd, joka updates the db state - then saves the state to the
> disk and uses the thing on the db as temp (just in case it crashes after the transaction is commited
> but before the state is written). Couldn't you write seeds during the transaction state, write the
> state file to disk, then close the transaction AFTER the file was written and secured?

## 20. Claude

The reordering is strictly better than commit-then-write, but it moves the gap rather than closing it.

| Ordering | Crash window | Result | Detectable |
|---|---|---|---|
| commit → write file | after commit | rows exist, state does not | no — next run duplicates |
| write file → commit | after file write | state exists, rows do not | yes — recorded PKs are not there |

Caveats: "secured" must mean `fsync` on the file and its directory, not a returned `write()`; and the
check does not hold for the natural-key path.

**The first idea is the stronger one.** Writing the state document to `joka_state` *inside* the
transaction closes the gap completely, without 2PC:

1. `BEGIN`
2. insert the rows
3. write the state document to `joka_state` — same transaction
4. `COMMIT` — rows and state durable together, no window
5. materialize the file from the committed document
6. if step 5 fails, the next run sees the document's sequence number ahead of the file's and rewrites

**2PC is the textbook close and is rejected.** `PREPARE TRANSACTION` needs `max_prepared_transactions
> 0` (defaults to `0`, needs a restart), and a dangling prepared transaction holds locks and blocks
vacuum indefinitely, including xid wraparound. Bad trade for a seed tool.

**The question it leaves:** in the hybrid the database copy is authoritative and the file is derived.
If the file is instead the artifact people edit or commit, that introduces a new conflict class.

---

## 21. Nick

> the reason for a file is that it allows you to quickly check database state against a record taht is
> not inside the thing it is auditing. Also, for a smallish data set it's not a problem but if you
> start to have 100s of megabytes... well, that's less realistic as an objection though.

## 22. Claude

That is the argument I should have been engaging with. Durability was the wrong axis.

### What DB-resident state structurally cannot do

State inside the database it describes is always self-consistent, so it can never report that the
database is the wrong one. Three cases only an external copy catches: a **restore** (a dump from
before the last sync rolls state back with the rows, so joka reads a state that agrees perfectly with
a database that silently lost seeds); a **substitution** (`DATABASE_URL` points at a stale container
or staging); and **`joka drop`** (the record of what was there goes with it).

With the file carrying database identity and a state sequence number:

| File vs database | Meaning |
|---|---|
| same identity, same version | normal |
| same identity, database behind | restored or rolled back |
| same identity, file behind | not materialized after the last run |
| different identity | not the database the file describes |

### Size: right to deflate it, and there is a better fix anyway

294 entities at ~1KB of applied values each is about 300KB; 100MB needs ~100,000 entities.

But: **the baseline only has to answer "did this change", not "to what."** Both values shown in a
prompt are read live. So store `sha256(value)` per column — ~64 bytes per column per entity, about
190KB for jjc2, independent of blob size.

### The reason to hash is secrets, not size

`resolveColumns` resolves `{{ asm.<source>.<key> }}` into the actual secret before inserting.
`PlanSyncAction` deliberately passes no `SecretResolver` so plaintext is never materialized into a
plan. Storing resolved values in state would undo that and put Secrets Manager plaintext into any
committed state file — terraform's state-contains-secrets problem. Hashing removes it rather than
managing it, and makes the state file safe to commit or attach to a bug report.

---

## 23. Nick

> the only thing I would add is that once you have that file, joka_state is no longer trustable and
> should be expunged.

## 24. Claude

Agreed — that makes `joka_state` a write-ahead journal rather than a store, and removes the
"which copy is authoritative" question rather than managing it. No journal row means the file is
current; a journal row present means the last run committed but did not materialize. Re-materializing
is idempotent.

Two consequences to decide deliberately.

**Keep a version marker in the database.** If nothing is left, restore detection dies with it — the
thing the file was for. `joka_meta` gets `state_identity` (UUID stamped on first write, travels with a
dump) and `state_version` (incremented in the transaction). A version number is not a second copy of
the state.

**Single-copy state inherits terraform's worst failure.** Lose the file and every entity reads as new;
the next sync inserts a second copy of everything. Terraform keeps `terraform.tfstate.backup` and
recovers with `import` per resource, which for joka is the adoption path that needs `_key`. The version
marker softens it: file missing but `state_version` is 17 is a refusal rather than 294 duplicate
inserts.

**The file needs exactly one home per database.** Two developers sync `dev1` from their own machines.
A syncs, writes their local file, expunges the journal. B has a stale file, detects it from the version
mismatch — and cannot recover, because the document B would need was expunged. So: `local` is one
database per developer with a local file; `dev1`/`e2e` are shared databases and the file must be
shared too, committed or in S3. Which is precisely why terraform grew remote backends.

---

## 25. Nick

> yes, write this up as a plan. also make a dump of our whole conversation somewhere please.

## 26. Claude

Wrote `proposal_entity_convergence_20260918.md` (thirteen decisions, the state artifact, six phases,
the mapping back to the twelve findings, five open questions) and this file.

---

## Corrections made during the discussion

Recorded because both changed the design rather than just the wording.

| Claim | Where | Correction |
|---|---|---|
| Terraform state is "deliberately not committed" | §10 | Overstated. Per-env committed state is a real and working arrangement (§12). The joka-specific objection is narrower: `local` is N databases per checkout, not one shared environment. |
| The insert gap "cannot be journaled away" for an external backend | §14 | Wrong. Writing and fsyncing state before `COMMIT` converts an undetectable failure into a detectable one (§16). The database backend is still the only one that needs no ordering discipline. |
| Tracking "started at table level" | §5 | It went file → row; the row tracking was subordinate to the file. Corrected in §6. |
| The original sync would "destroy" edited seed data | §5 | It never re-ran at all, so the destruction never happened; the cost was the opposite (§6). |
| Four advantages for database-resident state | §12 | Reduced to one — the in-transaction write — once the state became a single document (§18). |
| Durability was the axis for file versus database | §10, §14 | The real axis is audit independence: state inside the thing it audits cannot detect a restore or a substitution (§22). |
