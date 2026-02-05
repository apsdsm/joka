# libs
from typing import List
from sqlalchemy.ext.asyncio import AsyncConnection

# entities
from joka.entities.migration import Migration

# services
from joka.infra import db
from joka.infra import fs


# exception: migration chain broken
class MigrationChainBrokenError(Exception):
    """Raised when the migration chain is broken due to missing or out-of-order migrations."""
    pass


async def get_migration_chain(con: AsyncConnection, migrations_dir: str) -> List[Migration]:
    """
    Given a database connection and a migrations directory,
    return a list of Migration entities representing the full migration chain.
    Raise MigrationChainBrokenError if there are any inconsistencies.
    """

    # get the files waiting to be applied
    files = fs.list_migration_files(migrations_dir)
    models = await db.get_applied_migrations(con)

    idx = 0
    len_files = len(files)
    len_applied = len(models)
    migrations = []

    # iterate up through the indexes of both lists
    while True:
        if idx >= len_files and idx >= len_applied:
            break

        # if there is a file in the index, but no applied migration, it is unapplied
        if idx < len_files and idx >= len_applied:
            m = Migration(
                id=idx,
                migration_index=files[idx].index,
                applied_at="",
                file_name=files[idx].name,
                file_full_path=files[idx].full_path,
                status="pending"
            )
            migrations.append(m)
            idx += 1
            continue

        # if there is a file AND and migration in the index position 
        # and they have the same migration index, it is applied
        # if not, throw an error
        if idx < len_files and idx < len_applied:

            file = files[idx]
            model = models[idx]

            if file.index != model.migration_index:
                raise MigrationChainBrokenError(f"Migration chain broken at index {idx}: wanted {file.index} but found {model.migration_index}")

            m = Migration(
                id=idx,
                migration_index=file.index,
                applied_at=model.applied_at,
                file_name=file.name,
                file_full_path=file.full_path,
                status="applied"
            )
            migrations.append(m)
            idx += 1
            continue
                
        # if there is a db migration, but not file, raise a file missing error
        if idx >= len_files and idx < len_applied:
            raise MigrationChainBrokenError(f"Migration file missing for applied migration at index {idx}: {model.migration_index}")

    return migrations


async def apply(con: AsyncConnection, migration: Migration) -> None:
    """
    Apply a single migration to the database using the provided connection.
    """

    try:
        await db.apply_sql_from_file(con, migration.file_full_path)
        await db.record_migration_applied(con, migration.migration_index)

    except Exception as e:
        raise e