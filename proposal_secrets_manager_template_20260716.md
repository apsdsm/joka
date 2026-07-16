# Proposal: resolve secrets from AWS Secrets Manager in entity templates

Status: **implemented** (2026-07-16). See `internal/secrets`, `internal/domains/entity/app/resolve.go`, and the README "Secret sources for entity templates" section.

## Motivation

Entity seed files store secret material (client secrets, API keys) as hashes:

```yaml
client_secret: "{{ argon2id|dev_secret_jjc2_api }}"
key_hash:      "{{ sha256|lgc_sysadmin_cp_admin_key }}"
```

The argument after the `|` is a **literal** — so the real secret value lives in the YAML, which is
fine for local/throwaway data but unacceptable for any shared/live environment (dev/qa/prod): the
secret ends up committed to git.

Today the only workaround is an external "render" step: a shell script pulls the values from AWS
Secrets Manager, `envsubst`s `${PLACEHOLDER}`s into a gitignored copy of the entity files, and points
`joka --entities` at the rendered copy. That's two layers of templating and a script joka doesn't
know about — a footgun (running joka on the un-rendered files hashes the literal `${...}` string).

joka **already** talks to AWS Secrets Manager for connection passwords
(`internal/connection/aws.go`). This proposal makes entity templates able to pull a secret value
**directly**, so seed files never contain secrets and no render step is needed:

```yaml
client_secret: "{{ argon2id|asm.seed.jjc2_api_client_secret }}"
key_hash:      "{{ sha256|asm.seed.lgc_sysadmin_cp_admin_key }}"
```

## Current behavior (grounded)

- Template resolution: `internal/domains/entity/app/resolve.go` → `resolveValue(ctx, s, refMap, now, db)`.
  It dispatches on the inner expression of `{{ … }}`: `now`, `argon2id|<raw>`, `sha256|<raw>`,
  `lookup|table,col,where=val`, and `<ref>.id`. Each takes a **literal** arg.
- Planner: `isNonDeterministicTemplate` (resolve.go) marks `now` / `argon2id|…` as "regenerated" so
  their ever-changing values aren't diffed in `entity sync`/`--dry-run` output (`plan_sync.go`).
- Secrets Manager client: `internal/connection/aws.go` →
  `SecretFetcher.Fetch(ctx, secretID, region) (map[string]string, error)` (parses the JSON secret
  string into a flat key→value map via `parseSecretString`). Uses the default AWS credential chain.
- Config: `config/config.go` — `Config`/`Profile` carry a `Connection` whose `Secret{SecretID,
  Region, …}` describes where to pull the connection password. `Profile` overlays base config.

## Proposed feature

### 1. Named secret sources in config

Add a `secrets:` map (named sources) to `Config` and `Profile` (reusing the existing `Secret`
struct's `secret_id`/`region`). Profiles overlay it like other fields.

```yaml
# .jokarc.yaml
profiles:
  dev-remote:
    connection: { … }                 # existing
    secrets:
      seed:                            # a source name, referenced as asm.seed.*
        secret_id: lgc/seed/dev1
        region: ap-northeast-1
```

### 2. Template syntax

An `asm.<source>.<key>` reference resolves to the value of JSON key `<key>` in the secret configured
under `<source>`:

- **Hashed** (the common case): `{{ sha256|asm.seed.lgc_sysadmin_cp_admin_key }}`,
  `{{ argon2id|asm.seed.jjc2_user_web_secret }}` — resolve the secret value, then hash it.
- **Plaintext** (a non-hashed column): `{{ asm.seed.some_plain_value }}`.

`<source>` and `<key>` are dot-free identifiers (the secret_id — which may contain `/` — lives in
config, not the template, so `/` never appears in the ref). Any `sha256|`/`argon2id|` arg **not**
starting with `asm.` is treated as a literal exactly as today → **fully backward compatible**.

### 3. Semantics

- **Fetch once, cache per run.** Resolve each `<source>` on first use via the existing
  `SecretFetcher.Fetch`, cache the returned `map[string]string` keyed by source name for the whole
  command. (A seed run touches the same few secrets many times.)
- **Errors** (never include the secret value): unknown source → "secret source %q not configured";
  missing key → "key %q not found in secret source %q"; fetch failure → wrap the SM error.
- **Composition:** `asm.*` is only valid standalone or as the single argument to `sha256|`/`argon2id|`
  (not `lookup|`, not `.id`).

### 4. Security / planner

- **Never print resolved secret plaintext** anywhere — logs, errors, or `--dry-run`/plan Before/After.
- Treat any expression involving `asm.` as **non-deterministic/redacted** in the planner: extend
  `isNonDeterministicTemplate` so `asm.*` and `sha256|asm.*` / `argon2id|asm.*` are reported as
  "regenerated" (like `argon2id`), so their values are neither diffed nor displayed. (This also
  avoids a network fetch during planning.)

## Implementation sketch

1. **`config/config.go`** — add `Secrets map[string]config.Secret` to `Config` and `Profile`; merge
   in the profile overlay (base + profile, profile wins per source name).
2. **Secret resolver** — a small type (e.g. `internal/secrets`) wrapping the existing
   `connection.SecretFetcher` + a `map[string]map[string]string` per-run cache, with
   `Resolve(ctx, source, key) (string, error)`. Export/relocate `SecretFetcher` from
   `internal/connection` if needed so both connection and templates can use it.
3. **`internal/domains/entity/app/resolve.go`** — thread the resolver into `resolveValue`
   (add to the action's deps, like `db DBAdapter`). Add an `asm.` branch (standalone) and, inside the
   `sha256|`/`argon2id|` branches, if the arg starts with `asm.`, resolve it before hashing. Add a
   helper `parseSecretRef("asm.<source>.<key>") (source, key string, ok bool)`.
4. **`plan_sync.go` / `isNonDeterministicTemplate`** — redact `asm.`-derived values (see Security).
5. **Wiring** — build the resolver from `cfg.Secrets` in the sync/data commands (main.go) and pass it
   into `InsertGraphAction` / the plan.
6. **Tests** — resolve.go tests with a fake `SecretFetcher` (sha256|asm resolves+hashes; argon2id|asm;
   standalone asm; unknown source/key errors; literal args still work unchanged); a plan test proving
   no plaintext appears and asm exprs show as regenerated.

## Backward compatibility

Purely additive. Existing templates (`sha256|literal`, `argon2id|literal`, `now`, `lookup|…`,
`<ref>.id`) are untouched; `asm.` is a new, opt-in prefix. No migration required for existing users.

## What this unlocks for consumers (lgc_main / jjc2_main)

- `.jokarc.yaml` gains a `secrets:` source (e.g. `seed → lgc/seed/dev1`).
- Dev/qa/prod entity files reference `{{ sha256|asm.seed.<key> }}` instead of `${PLACEHOLDER}`.
- **Deletes** the render workaround: `render-seed.sh`, the gitignored `_rendered/` tree, and the
  envsubst step in `db-seed-remote.sh`. joka reads the per-env entity dir directly under the profile
  that configures the secret source. Local keeps literal throwaway secrets (no `asm.`).
```
