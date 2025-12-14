import os
from typing import List
from sqlalchemy import text
from sqlalchemy.ext.asyncio import create_async_engine, AsyncEngine, AsyncConnection

from joka.models.migration_model import MigrationModel


# exception: no migration table
class NoMigrationTableError(Exception):
    """Raised when the migrations table does not exist in the database."""
    pass

# exception: migration table already exists
class MigrationAlreadyExistsError(Exception):
    """Raised when attempting to create a migrations table that already exists."""
    pass



# exception: error creating migration table
class MigrationTableCreationError(Exception):
    """Raised when there is an error creating the migrations table."""
    pass




def make_engine(database_url: str | None = None) -> AsyncEngine:
    """
    Create a SQLAlchemy engine for the given database URL.
    If no URL is provided, use the DATABASE_URL environment variable.
    """

    db_url = database_url or os.getenv("DATABASE_URL", "")

    if not db_url:
        raise RuntimeError("DATABASE_URL is not set")

    return create_async_engine(db_url, echo=False, pool_pre_ping=True)



async def has_migrations_table(con: AsyncConnection) -> bool:
    """
    Check if the migrations table exists in the database.
    Returns True if it exists, False otherwise.
    """
    
    sql = text(
        """
        SELECT 1
        FROM information_schema.tables
        WHERE table_name = 'migrations'
        """
    )

    res = await con.execute(sql)
    return res.scalar() == 1



async def create_migrations_table(engine: AsyncEngine) -> None:
    """
    Create the migreations table - throw exception if already exists
    """
    async with engine.connect() as con:

        # ensure the migrations table does not already exist
        if await has_migrations_table(con):
            raise MigrationAlreadyExistsError("Migrations table already exists.")
        
        # create migrations table
        sql = text(
            """
            CREATE TABLE migrations
            (
                id INT AUTO_INCREMENT PRIMARY KEY,
                migration_index VARCHAR(255) NOT NULL UNIQUE,
                applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
            );
            """
        )

        res = await con.execute(sql)

        if res.rowcount != None and res.rowcount < 0:
            raise MigrationTableCreationError("Error creating migrations table.")

        await con.commit()



async def get_applied_migrations(con: AsyncConnection) -> List[MigrationModel]:
    """
    Retrieve the set of applied migrations from the database.
    If the migrations table does not exist, raise NoMigrationTableError.
    Otherwise returns a set of applied migration filenames.
    """
   
    if not await has_migrations_table(con):
        raise NoMigrationTableError("Migrations table does not exist.")
            
    sql = text(
        """
        SELECT *
        FROM migrations
        """
    )

    res = await con.execute(sql)
    rows = res.fetchall()
    migrations = [MigrationModel(**row._mapping) for row in rows]
    
    return migrations



async def apply_sql_from_file(con: AsyncConnection, file_path: str) -> None:
    """
    Apply the SQL commands from the given file to the database using the provided connection.
    """

    with open(file_path, "r") as f:
        sql_commands = f.read()

    sql = text(sql_commands)
    await con.execute(sql)



async def record_migration_applied(con: AsyncConnection, migration_index: str) -> None:
    """
    Record that a migration has been applied by inserting a record into the migrations table.
    If there are any errors, an exception will be raised.
    """

    sql = text(
        """
        INSERT INTO migrations (migration_index)
        VALUES (:migration_index)
        """
    )

    # if any errors occur, they will raise exceptions
    await con.execute(sql, {"migration_index": migration_index})
