# ext libs
from sqlalchemy.ext.asyncio import AsyncEngine
from rich import print as rprint

# exceptions
from joka.services.db import NoMigrationTableError

# services
import joka.services.migrations as migrations

async def run(engine: AsyncEngine, migrations_dir: str) -> None:
    """
    Show the status of all migrations.
    """

    async with engine.connect() as conn:
        try:
            # let the user what we're going to do
            rprint("[green]Checking migration chain...[/green]")

            # get the files waiting to be applied
            migrations_chain = await migrations.get_migration_chain(conn, migrations_dir)

            # if no migration files found, inform user and exit
            if len(migrations_chain) == 0:
                rprint("No migration files found.")
                return

            # print all the migrations
            for m in migrations_chain:
                rprint(f"Migration {m.migration_index} - Status: {m.status}")

        except NoMigrationTableError:
            rprint("[red]Migrations table does not exist.[/red]")

        except Exception as e:
            rprint(f"[red]Error checking migration status: {e}[/red]")

        finally:
            await conn.close()