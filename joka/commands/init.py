# ext libs
from sqlalchemy.ext.asyncio import AsyncEngine
from rich import print as rprint

# exceptions
from joka.infra.db import MigrationAlreadyExistsError, MigrationTableCreationError

# services
from joka.infra.db import create_migrations_table

async def run(engine: AsyncEngine) -> None:
    """
    Initialize the migrations system by creating the migrations table.
    """

    async with engine.connect() as conn:
        try:
            # let the user know what we're going to do
            rprint("[green]Initializing migrations system...[/green]")

            # try initializing the migrations table
            await create_migrations_table(engine)

        except MigrationAlreadyExistsError:
            rprint("[yellow]Migrations table already exists.[/yellow]")   
        
        except MigrationTableCreationError:
            rprint("[red]Error creating migrations table.[/red]")

        except Exception as e:
            rprint(f"[red]Unexpected error: {e}[/red]")

        finally:
            await conn.close()