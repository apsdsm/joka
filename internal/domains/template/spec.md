# Template

Syncs seed/template data from files on disk into database tables. Tables and their sync strategies are declared in a `_config.yaml` file. Each table has a directory of record files (YAML for single rows, CSV for multiple rows).

## domain

- `StrategyType` — enum for sync strategies: `truncate` (delete all + re-insert), `update` (upsert, not yet implemented), `delete` (remove absent rows, not yet implemented).
- `RecordType` — enum for record file formats: `row` (YAML) or `list` (CSV).
- `Record` — a single data file within a table directory.
- `Table` — aggregate combining config (name, strategy) with discovered record files.
- Sentinel errors: `ErrTableNotFound`.

## app

- `LoadTableDataAction` — reads all record files for a table and flattens them into a single slice of row maps ready for database insertion.

## infra

### data model

No dedicated table. Operates on user-defined tables via dynamic SQL (`TRUNCATE TABLE`, `INSERT INTO`). Table existence is verified before each operation.

Infra models: `TableConfig` (per-table entry from `_config.yaml`), `TemplatesConfig` (top-level config structure).

### file structure

Template files live in a configurable directory (default `devops/templates/`). Structure:

```
devops/templates/
├── _config.yaml          # declares tables and strategies
├── <table_name>/
│   ├── record.yaml       # single row (YAML keys = columns)
│   ├── record.csv        # multiple rows (CSV headers = columns)
│   └── ...
```

The `_config.yaml` format:

```yaml
name: <config_name>
tables:
  - name: <table_name>
    strategy: truncate|update|delete
```
