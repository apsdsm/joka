import os
from datetime import datetime

from joka.infra import fs

async def run(migrations_dir: str, migration_name: str) -> None:
    """
    Create a new migration file in the migrations directory.
    """

    try:

        # let the user know what we're going to do
        print(f"[green]Creating new migration file '{migration_name}' in '{migrations_dir}'...[/green]")

        # create the migration file
        fs.create_migration_file(migrations_dir, migration_name)


    except fs.MigrationDirNotFoundError as e:
        print(f"[red]Error: {e}[/red]")