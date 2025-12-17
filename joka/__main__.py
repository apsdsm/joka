import asyncio
import os
import typer

# libs
from dotenv import load_dotenv
from sqlalchemy.ext.asyncio import AsyncEngine
from dataclasses import dataclass
from rich import print as rprint


# subcommands
from joka.services import db
from joka import subcommands

# the application state object
@dataclass
class AppState:
    env_path: str = ".env"
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

@app.command()
def make(migration_name: str):
    asyncio.run(subcommands.make.run(migrations_dir=state.migrations_dir, migration_name=migration_name))

###
### callbacks
###

@app.callback()
def main(
    env: str = typer.Option(
        ".env", 
        "--env", 
        "-e",
        help="Path to the environment file (default is .env)"
        ),
    migrations: str = typer.Option(
        "devops/db/migrations", 
        "--migrations", 
        "-m",
        help="Path to the migrations directory (default is devops/migrate/migrations)"
        ),
    auto: bool = typer.Option(
        False, 
        "--auto",
        "-a",
        help="Automatically confirm prompts (use with caution)"
        ),
):
    
    # set up state
    state.automode = auto
    state.env_path = env
    state.migrations_dir = migrations

    # if a env file is specified but it doesn't exits, stop proccesing
    if not state.env_path == ".env" and not os.path.isfile(state.env_path):
        rprint(f"[red]Unable to find specified .env file: {state.env_path}[/red]")
        raise typer.Exit(code=1)

    # try load env vars
    load_dotenv(state.env_path)

    # ensure we have the db url
    db_url = os.getenv("DATABASE_URL")

    if not db_url:
        rprint("[red]DATABASE_URL not found in environment variables.[/red]")
        raise typer.Exit(code=1)

    # try create the db engine
    try:
        state.db_engine = db.make_engine(db_url)

    except Exception as e:
        rprint(f"[red]Error creating database engine: {e}[/red]")
        raise typer.Exit(code=1)


if __name__ == "__main__":
    app()