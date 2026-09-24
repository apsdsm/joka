# Bug: `entity sync` reports "already synced" for files that `entity status` flags as `[modified]`

**Date:** 2026-06-01
**Version:** joka 0.9.0
**Profile in use:** `dev-remote` (aws_secrets_manager source, MySQL 8 over an SSM tunnel on 127.0.0.1:3307)

## Summary

`joka entity sync` skips files whose content has changed since the last sync and reports them as already synced, even though `joka entity status` correctly identifies them as `[modified]`. Field-level modifications to existing entities are silently ignored. The only way to push the change is `joka entity reimport <file>`, which is destructive (delete + re-insert).

The naming implies `sync` should reconcile state — propagating modifications is what most users will expect a command called `sync` to do.

## Reproduction

1. Have an entity file already synced to the DB (e.g. `clients/lgc_sysadmin_cp_client.yaml`).
2. Modify a non-key field in the YAML — in our case the `redirect_uris` JSON string on a single row:

   ```yaml
   # before
   redirect_uris: '["http://localhost:40203/callback","http://cp:40203/callback","http://localdev:40203/callback","https://cp.dev1.login-dev.jinjicrew.jp/callback"]'
   # after
   redirect_uris: '["http://localhost:40203/callback","http://cp:40203/callback","http://localdev:40203/callback","https://dev1-cp.login-dev.jinjicrew.jp/callback"]'
   ```

3. Run `joka --profile dev-remote entity sync`.
4. Run `joka --profile dev-remote entity status`.

## Actual output

```
$ joka --profile dev-remote entity sync
All entity files already synced.

$ joka --profile dev-remote entity status

Entity file status:
  [synced]    clients/jjc2_admin_web_client.yaml
  [synced]    clients/jjc2_sysadmin_cp_client.yaml
  [synced]    clients/jjc2_system_api_client.yaml
  [modified]  clients/lgc_sysadmin_cp_client.yaml
  [synced]    persons/sysadmin_person.yaml
```

`status` shows the file as `[modified]` but `sync` declares "already synced" and the DB row is unchanged. Verified the DB still had the old `redirect_uris` value via a `SELECT` over the tunnel.

## Expected behaviour

One of these:

1. **`sync` propagates modifications** (most natural reading of the name). When the file is `[modified]` and only field-level values changed (no primary-key rename, no entity removed), upsert the existing row(s) with the new field values.
2. **`sync` documents that it only handles new entries**, prints a warning when there are `[modified]` files, and points the user at the right command. E.g. `note: 1 file modified ([…path…]); use 'entity reimport <file>' to apply changes.`

Today there is no signal from `sync` that anything was skipped, and no docstring hint that modifications require a different command.

## Workaround used

```
joka --profile dev-remote entity reimport devops/entities/clients/lgc_sysadmin_cp_client.yaml -a
```

This worked, but `reimport` is destructive (drop + re-insert) which is heavier than needed for a single-field change and can be unsafe when external rows reference the deleted rows by internal id. An upsert path inside `sync` would be much safer for routine seed-data changes.

## Notes

- The behaviour was surprising specifically because `entity status` clearly knows the file is modified — i.e. the change-detection logic exists; `sync` just doesn't act on it.
- `entity update` (per `--help`: "Add new entities from a file without deleting existing rows") also doesn't help here — the entity already exists; we want to modify it.
- Project context: this surfaced while shipping the LGC dev1 cluster, where a Terraform-driven hostname rename (`cp.dev1.*` → `dev1-cp.*`) required updating the OAuth client's `redirect_uris` field. Routine config drift that should be easy to push.

## Suggested fix direction

Inside `entity sync`'s file loop, treat `[modified]` files the same as `[unsynced]` but use an UPSERT path (or per-row diff-and-update) instead of a destructive re-insert. The unsynced/new path can keep its current INSERT semantics. If a structural change is detected (entity removed from YAML, primary-key changed, child entity tree restructured), fall back to recommending `reimport` rather than doing it automatically.
