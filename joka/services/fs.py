from pathlib import Path
from typing import List
import re

from models.migration_file import MigrationFile

MIGRATION_PATTERN = re.compile(r"^(\d{12})_.*\.sql$")  # yymmddhhmmss_*.sql

# excepton: migration dir not found
class MigrationDirNotFoundError(Exception):
    """Raised when the specified migrations directory does not exist."""
    pass


def list_migration_files(dir: str) -> List[MigrationFile]:
    """
    List all migration files in the given directory, sorted by name.
    """

    dir_path = Path(dir)

    # ensure the path exists
    if not dir_path.exists() or not dir_path.is_dir():
        raise MigrationDirNotFoundError(f"Migrations directory not found: {dir}")
    
    # make a list of MigrationFile
    migration_files: List[MigrationFile] = []

    for f in dir_path.iterdir():
        if f.is_file() and f.suffix == ".sql":
            match = MIGRATION_PATTERN.match(f.name)
            if match:
                index = match.group(1)
                name = f.name[len(index) + 1:-4]  # remove index and .sql
                migration_files.append(
                    MigrationFile(
                        index=index,
                        name=name,
                        full_path=str(f.resolve())
                    )
                )

    # sort by index
    migration_files.sort(key=lambda mf: mf.index)

    return migration_files


def extract_migration_name(file_path: Path) -> str | None:
    """
    Extract the migration name from the given file path.
    """
    pass  # to do


def load_sql_statements(file_path: Path) -> List[str]:
    """
    Load SQL statements from the given migration file.
    """
    pass  # to do