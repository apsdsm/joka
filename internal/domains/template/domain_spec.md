# Template Domain

Manages syncing seed/template data from files on disk to database tables. Useful for maintaining reference data (email templates, feature flags, default settings) that should be version-controlled and deployable.

## Directory Structure

Templates live in the templates directory (`devops/templates/` by default):

```
devops/templates/
├── _config.yaml          # Declares which tables to sync and their strategy
├── email_templates/      # One directory per table
│   ├── welcome.yaml      # YAML file = single row
│   └── reminder.yaml
└── settings/
    └── defaults.csv      # CSV file = multiple rows
```

### `_config.yaml`

Declares the tables and their sync strategies:

```yaml
name: app_data
tables:
  - name: email_templates
    strategy: truncate
  - name: settings
    strategy: truncate
```

Each table entry must have a corresponding subdirectory with the same name.

### Record Files

Two formats are supported:

**YAML** (`.yaml` / `.yml`) — Each file represents a single row. Keys are column names.

```yaml
subject: Welcome
body: Hello, welcome to the app!
active: true
```

**CSV** (`.csv`) — Each file represents multiple rows. First row is column headers.

```csv
key,value
timeout,30
max_retries,3
```

Files with other extensions are silently ignored.

## Sync Strategies

| Strategy | Behavior | Status |
|----------|----------|--------|
| `truncate` | Delete all existing rows, then insert from files | Implemented |
| `update` | Upsert/merge with existing data | Not yet implemented |
| `delete` | Delete rows not present in files | Not yet implemented |

If no strategy is specified in `_config.yaml`, it defaults to `update`.

## Sync Flow

When `data sync` runs:

1. **Load config** — Parse `_config.yaml` to get the list of tables and strategies.
2. **Discover records** — For each table, scan its subdirectory for `.yaml` and `.csv` files.
3. **Preview** — Print each table with its strategy, row count, and file count. Prompt for confirmation.
4. **Sync** — Inside a single transaction, for each table:
   - Load all record files into `[]map[string]any` (column name to value).
   - Execute the strategy (currently only truncate: `TRUNCATE TABLE` then `INSERT` all rows).
5. **Commit or rollback** — If any table fails, the entire sync is rolled back.

The command acquires an advisory lock (via the lock domain) before starting.

## Layer Responsibilities

### `domain/`
Pure data types and constants.

- `StrategyType` — Enum: `truncate`, `update`, `delete`.
- `RecordType` — Enum: `row` (YAML, single row) or `list` (CSV, multiple rows).
- `Record` — A single data file (name, path, type).
- `Table` — A configured table with its name, strategy, and list of records.
- `ErrTableNotFound` — Returned when a table referenced in config doesn't exist in the database.

### `app/`
Use-case actions.

- `LoadTableDataAction` — Loads all record files for a table and combines them into a flat list of rows.
- `SyncTableAction` — Loads data, then truncates and inserts (for truncate strategy).
- `DBAdapter` — Interface for `TruncateTable` and `InsertRows`.

### `infra/`
Infrastructure implementations.

- `GetTables()` — Reads `_config.yaml`, discovers subdirectories and record files, returns `[]Table`.
- `LoadRecord()` — Parses a single YAML or CSV file into `[]map[string]any`.
- `MySQLDBAdapter` — Implements `DBAdapter` with dynamic SQL (column names from map keys, parameterized values).
- `models/` — `TemplatesConfig` and `TableConfig` for YAML unmarshaling.

## Commands

| Command | What it does |
|---------|-------------|
| `joka data sync` | Syncs all configured tables from files to database (with locking) |
