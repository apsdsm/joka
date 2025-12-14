import argparse
import asyncio
import os
import typer

# libs
from dotenv import load_dotenv
from sqlalchemy.ext.asyncio import AsyncEngine
from dataclasses import dataclass

# subcommands
from services import db
import subcommands

@dataclass
class AppState:
    env_path: str = ""
    db_engine: AsyncEngine | None = None
    migrations_dir: str = ""
    automode: bool = False

# the main app
app = typer.Typer()
state = AppState()


###
### commands
###


@app.command()
def init():
    asyncio.run(subcommands.init.run(engine=state.db_engine))
    
@app.command()
def up():
    asyncio.run(subcommands.up.run(engine=state.db_engine, migrations_dir=state.migrations_dir))

@app.command()
def status():
    asyncio.run(subcommands.status.run(engine=state.db_engine, migrations_dir=state.migrations_dir))

###
### callbacks
###


@app.callback()
def main(
    env: str = typer.Option(".env", help="Path to the environment file (default is .env)"),
    migrations: str = typer.Option("devops/db/migrations", help="Path to the migrations directory (default is devops/migrate/migrations)"),
    auto: bool = typer.Option(False, help="Automatically confirm prompts (use with caution)"),
):
    
    # set up state
    state.automode = auto
    state.env_path = env
    state.migrations_dir = migrations

    # set up env
    load_dotenv(state.env_path)

    # set up db engine
    state.db_engine = db.make_engine(os.getenv("DATABASE_URL"))


if __name__ == "__main__":
    app()