# libs
from typing import List, Dict, Any
from pathlib import Path
import yaml
import csv

from joka.files.models import Table, Record, RecordType, TemplatesConfig, StrategyType


class TemplatesDirectoryNotFoundError(Exception):
    """Raised when the specified templates directory does not exist."""
    pass


class TemplatesConfigNotFoundError(Exception):
    """Raised when the templates config file is missing."""
    pass


class TableDirectoryNotFoundError(Exception):
    """Raised when a configured table directory does not exist."""
    pass


def get_tables(templates_dir: str) -> List[Table]:
    """
    Get a list of tables with their records from the templates directory.
    Uses _config.yaml to determine which tables to sync and their strategies.
    """
    p = Path(templates_dir)

    if not p.exists() or not p.is_dir():
        raise TemplatesDirectoryNotFoundError(f"Templates directory not found: {templates_dir}")

    config_path = p / "_config.yaml"
    if not config_path.exists() or not config_path.is_file():
        raise TemplatesConfigNotFoundError(f"Templates config file not found: {config_path}")

    # Parse config
    with open(config_path, "r") as f:
        config_data = yaml.safe_load(f)
        config = TemplatesConfig.model_validate(config_data)

    # Build tables from config
    tables = []
    for table_config in config.tables:
        table_path = p / table_config.name

        if not table_path.exists() or not table_path.is_dir():
            raise TableDirectoryNotFoundError(
                f"Table directory not found: {table_path} (configured in _config.yaml)"
            )

        # Discover records in the table directory
        records = []
        for f in table_path.iterdir():
            if f.is_file():
                match f.suffix:
                    case ".csv":
                        record_type = RecordType.list
                    case ".yaml" | ".yml":
                        record_type = RecordType.row
                    case _:
                        continue

                records.append(Record(
                    name=f.stem,
                    path=str(f),
                    type=record_type
                ))

        tables.append(Table(
            name=table_config.name,
            path=str(table_path),
            strategy=table_config.strategy,
            records=records
        ))

    return tables


def load_record(record: Record) -> List[Dict[str, Any]]:
    """
    Load a record file and return its data as a list of row dictionaries.
    - YAML files (row type): Returns a single-item list with the YAML content
    - CSV files (list type): Returns a list of dicts, one per row
    """
    if record.type == RecordType.row:
        with open(record.path, "r") as f:
            data = yaml.safe_load(f)
            # YAML row is a single dict, wrap in list
            return [data] if data else []

    elif record.type == RecordType.list:
        with open(record.path, "r", newline="") as f:
            reader = csv.DictReader(f)
            return list(reader)

    return []


def load_table_data(table: Table) -> List[Dict[str, Any]]:
    """
    Load all records for a table and return as a flat list of row dictionaries.
    """
    rows = []
    for record in table.records:
        rows.extend(load_record(record))
    return rows
