from sqlalchemy.ext.asyncio import AsyncEngine
from rich import print as rprint

import joka.services.templates as templates_service
from joka.infra import db, cli
from joka.files.models import StrategyType


async def run(engine: AsyncEngine, templates_dir: str, auto_confirm: bool = False) -> None:
    """
    Sync template data to the database.
    """
    # Load templates configuration
    tables = templates_service.get_tables(templates_dir)

    if not tables:
        rprint("[yellow]No tables configured for sync.[/yellow]")
        return

    # Show what will be synced
    rprint("\n[bold]Tables to sync:[/bold]")
    for table in tables:
        row_count = sum(len(templates_service.load_record(r)) for r in table.records)
        rprint(f"  [cyan]{table.name}[/cyan] ({table.strategy.value}): {row_count} rows from {len(table.records)} files")

    rprint("")

    # Confirm
    if not auto_confirm:
        if not cli.confirm("Proceed with sync? (only 'yes' will confirm): "):
            rprint("[yellow]Sync cancelled.[/yellow]")
            return

    # Sync each table
    async with engine.connect() as con:
        for table in tables:
            if table.strategy == StrategyType.truncate:
                await sync_truncate(con, table)
            else:
                rprint(f"[yellow]Strategy '{table.strategy.value}' not yet implemented for {table.name}, skipping.[/yellow]")

        await con.commit()

    rprint("\n[green]Sync complete.[/green]")


async def sync_truncate(con, table) -> None:
    """
    Sync a table using the truncate strategy:
    1. Truncate the table (delete all rows)
    2. Insert all rows from the template files
    """
    rprint(f"[cyan]Syncing {table.name}...[/cyan]")

    # Load all data for this table
    rows = templates_service.load_table_data(table)

    # Truncate
    await db.truncate_table(con, table.name)
    rprint(f"  Truncated {table.name}")

    # Insert
    if rows:
        count = await db.insert_rows(con, table.name, rows)
        rprint(f"  Inserted {count} rows")
    else:
        rprint(f"  No rows to insert")
