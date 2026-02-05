# ext libs
from sqlalchemy.ext.asyncio import AsyncEngine
from rich import print as rprint

# errors
from joka.infra.db import NoMigrationTableError

# services
import joka.services.migrations as migrations
import joka.infra.cli as cli

async def run(engine: AsyncEngine, migrations_dir: str) -> None:
    """
    Apply all pending migrations.
    """

    async with engine.connect() as conn:
        try:
            # let the user what we're going to do
            rprint("[green]Checking migration chain...[/green]")

            # get the files waiting to be applied
            migrations_chain = await migrations.get_migration_chain(conn, migrations_dir)

            # now print all the migrations
            for m in migrations_chain:
                print(f"Migration {m.migration_index} - Status: {m.status}")

            # get confirmation to apply pending migrations
            pending_migrations = [m for m in migrations_chain if m.status == "pending"]
            if not pending_migrations:
                print("No pending migrations to apply.")
                return
            
            # confirm with user
            if not cli.confirm(prompt=f"{len(pending_migrations)} pending migrations found. Apply now? (only 'yes' will apply): "):
                print("Migration aborted by user.")
                return
            
            # apply pending migrations
            for m in pending_migrations:
                print(f"Applying migration {m.migration_index}...")
                await migrations.apply(conn, m)
        
        except NoMigrationTableError:
            rprint("[red]Migrations table does not exist.[/red]")
        
        except Exception as e:
            rprint(f"[red]Error applying migrations: {e}[/red]")

        finally:
            await conn.close()