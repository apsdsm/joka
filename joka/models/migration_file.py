from dataclasses import dataclass

@dataclass
class MigrationFile:
    """
    Representation of a migration file.
    """
    index: str # migration index extracted from file name
    name: str # migration name extracted from file name
    full_path: str # full path to the migration file