from dataclasses import dataclass
from typing import List
from enum import Enum

# entities
from joka.models.record import Record

class StrategyType(Enum):
    delete = "delete"
    update = "update"
    truncate = "truncate"

@dataclass
class Table:
    """
    Representation of a database table. Includes the state data that shuold
    exist in that table.
    """
    name: str  # Name of the table
    path: str = "" # Path to the table state data directory
    strategy: StrategyType = StrategyType.update  # Strategy for syncing the table
    records: List[Record] = None  # List of records in the table