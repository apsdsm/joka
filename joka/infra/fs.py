import re

from pathlib import Path
from typing import List
from datetime import datetime

from joka.files.models import MigrationFile

MIGRATION_PATTERN = re.compile(r"^(\d{12})_.*\.sql$")  # yymmddhhmmss_*.sql

# exception: migration directory was missing
class MigrationDirectoryNotFoundError(Exception):
    """Raised when the specified migrations directory does not exist."""
    pass

# exception: migration dir not found
class MigrationDirNotFoundError(Exception):
    """Raised when the specified migrations directory does not exist."""
    pass


def create_migration_file(migration_dir: str, migration_name: str) -> None:
    """
    Create a new migration file in the specified directory with the given name.
    """

    dir_path = Path(migration_dir)

    # check if path exists or throw exception
    if not dir_path.exists():
        raise MigrationDirNotFoundError(f"Migrations directory not found: {migration_dir}")

    # create a new migration file with a timestamp
    timestamp = datetime.now().strftime("%Y%m%d%H%M%S")
    migration_filename = f"{timestamp}_{migration_name}.sql"
    migration_filepath = dir_path / migration_filename

    with migration_filepath.open("w") as f:
        f.write("-- Write your migration SQL here\n")


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
