from dataclasses import dataclass
from typing import List
from enum import Enum
from pydantic import BaseModel


@dataclass
class MigrationFile:
    """
    Representation of a migration file on disk.
    """
    index: str  # migration index extracted from file name
    name: str  # migration name extracted from file name
    full_path: str  # full path to the migration file


class RecordType(Enum):
    """
    RecordType is the different sort of record that is stored in the file.
    The way the data is imported depends on the type.
    """
    row = "row"
    list = "list"


@dataclass
class Record:
    """
    Representation of a single record file in a table's template data.
    """
    name: str  # Unique identifier for the record
    path: str  # Path to the record file
    type: RecordType  # Type of the record


class StrategyType(str, Enum):
    """Strategy for syncing table data."""
    delete = "delete"
    update = "update"
    truncate = "truncate"


class TableConfig(BaseModel):
    """
    Configuration for a table from _config.yaml.
    """
    name: str
    strategy: StrategyType = StrategyType.update


@dataclass
class Table:
    """
    Representation of a database table with its template records to sync.
    """
    name: str  # Name of the table
    path: str  # Path to the table's template directory
    strategy: StrategyType  # Strategy for syncing the table
    records: List[Record]  # List of records in the table


class TemplatesConfig(BaseModel):
    """
    Configuration loaded from _config.yaml in the templates directory.
    """
    name: str  # Name of the configuration
    tables: List[TableConfig]  # List of table configs
