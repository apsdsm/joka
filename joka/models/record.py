from dataclasses import dataclass
from enum import Enum

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
    Representation of a single record in a table's state data.
    """

    name: str  # Unique identifier for the record
    path: str  # Path to the record state data file
    type: RecordType  # Type of the record (e.g., "json", "yaml", etc.)