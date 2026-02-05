import asyncio
import os
import typer

# libs
from dotenv import load_dotenv
from sqlalchemy.ext.asyncio import AsyncEngine
from dataclasses import dataclass
from rich import print as rprint


# subcommands
from joka.infra import db
from joka import commands

# the application state object
@dataclass
class AppState:
    env_path: str = ".env"
    db_engine: AsyncEngine | None = None
    migrations_dir: str = ""
    automode: bool = False
    templates_dir: str = ""

# the main app
app = typer.Typer()
sub_app_migrate = typer.Typer()
sub_app_make = typer.Typer()
sub_app_data = typer.Typer()

app.add_typer(sub_app_migrate, name="migrate", help="Database migration commands")
app.add_typer(sub_app_data, name="data", help="Application data state commands")
app.add_typer(sub_app_make, name="make", help="Create new migration files")

state = AppState()

###
### init commands
###

@app.command()
def init():
    asyncio.run(commands.init.run(engine=state.db_engine))

### 
### migrate commands
###

@sub_app_migrate.command()
def up():
    asyncio.run(commands.migrate.up.run(engine=state.db_engine, migrations_dir=state.migrations_dir))

@sub_app_migrate.command()
def status():
    asyncio.run(commands.migrate.status.run(engine=state.db_engine, migrations_dir=state.migrations_dir))

###
### make commands
###

@sub_app_make.command()
def make(migration_name: str):
    asyncio.run(commands.make.run(migrations_dir=state.migrations_dir, migration_name=migration_name))


###
### data commands
###

@sub_app_data.command()
def sync():
    asyncio.run(commands.data.sync.run(
        engine=state.db_engine,
        templates_dir=state.templates_dir,
        auto_confirm=state.automode
    ))


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
        "devops/migrations", 
        "--migrations", 
        "-m",
        help="Path to the migrations directory (default is devops/migrations)"
        ),
    auto: bool = typer.Option(
        False, 
        "--auto",
        "-a",
        help="Automatically confirm prompts (use with caution)"
        ),
    templates: str = typer.Option(
        "devops/templates",
        "--templates",
        "-t",
        help="Path to the templates directory (default is devops/templates)"
        ),
    ):
    
    # set up state
    state.automode = auto
    state.env_path = env
    state.migrations_dir = migrations
    state.templates_dir = templates

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