import os
from typing import List, Dict, Any
from sqlalchemy import text
from sqlalchemy.ext.asyncio import create_async_engine, AsyncEngine, AsyncConnection

from joka.db.models import MigrationRow


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



async def get_applied_migrations(con: AsyncConnection) -> List[MigrationRow]:
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
    migrations = [MigrationRow(**row._mapping) for row in rows]
    
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


class TableNotFoundError(Exception):
    """Raised when attempting to operate on a table that does not exist."""
    pass


async def table_exists(con: AsyncConnection, table_name: str) -> bool:
    """Check if a table exists in the database."""
    sql = text(
        """
        SELECT 1
        FROM information_schema.tables
        WHERE table_name = :table_name
        """
    )
    res = await con.execute(sql, {"table_name": table_name})
    return res.scalar() == 1


async def truncate_table(con: AsyncConnection, table_name: str) -> None:
    """
    Truncate a table, removing all rows.
    Raises TableNotFoundError if the table does not exist.
    """
    if not await table_exists(con, table_name):
        raise TableNotFoundError(f"Table does not exist: {table_name}")

    # Use backticks to quote the table name for MySQL
    sql = text(f"TRUNCATE TABLE `{table_name}`")
    await con.execute(sql)


async def insert_rows(con: AsyncConnection, table_name: str, rows: List[Dict[str, Any]]) -> int:
    """
    Insert rows into a table. Each row is a dict mapping column names to values.
    Returns the number of rows inserted.
    Raises TableNotFoundError if the table does not exist.
    """
    if not rows:
        return 0

    if not await table_exists(con, table_name):
        raise TableNotFoundError(f"Table does not exist: {table_name}")

    # Get column names from the first row
    columns = list(rows[0].keys())
    col_names = ", ".join(f"`{c}`" for c in columns)
    placeholders = ", ".join(f":{c}" for c in columns)

    sql = text(f"INSERT INTO `{table_name}` ({col_names}) VALUES ({placeholders})")

    for row in rows:
        await con.execute(sql, row)

    return len(rows)
