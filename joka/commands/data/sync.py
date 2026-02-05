
from sqlalchemy.ext.asyncio import AsyncEngine

from rich import print as rprint

# services
import joka.services.state as state_service

async def run(engine: AsyncEngine, state_dir: str) -> None:
    """
    Sync the data state.
    """

    rprint("[green]Sync command not yet implemented.[/green]")

    tables = state_service.get_state(state_dir)

    # print them out on new lines for now
    for table in tables:
        rprint(f"|- Table: {table.name}, {table.path} ({len(table.records)} records)")

        for record in table.records:
            rprint(f"|  |-  Record: {record.type} - {record.name} - {record.path}")
