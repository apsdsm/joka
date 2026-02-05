# libs
from typing import List
from pathlib import Path
import yaml

from joka.models.table import Table
from joka.models.record import Record, RecordType
from joka.models.schema import Schema


class DataStateDirectoryNotFoundError(Exception):
    """Raised when the specified state data directory does not exist."""
    pass

class DataStateConfigNotFoundError(Exception):
    """Raised when the state data config file is missing."""
    pass

def get_state(state_dir: str) -> List[Table]:
    """
    Get a list of state stems (directory names) in the given state directory.
    """

    # ensure the state dir exists
    p = Path(state_dir)

    if not p.exists() or not p.is_dir():
        raise DataStateDirectoryNotFoundError(f"State data directory not found: {state_dir}")

    # ensure a config file (_config.yaml) exists in the state dir
    config_path = p / "_config.yaml"

    if not config_path.exists() or not config_path.is_file():
        raise DataStateConfigNotFoundError(f"State data config file not found: {config_path}")


    # read the yaml config file, and parse into a config object.
    with open(config_path, "r") as f:
        config_data = yaml.safe_load(f)
        config = Schema.model_validate(config_data)


    # print out the contents of the config for now
    print(config)


    # now do the other things for now, which will part of our validation

    # bulid list of tables
    tables = list()


    for d in p.iterdir():
        if d.is_dir():
            t = Table(
                path=str(d),
                name=d.name,
                records=[]
            )
            tables.append(t)

    # for each table, get records
    for t in tables:
        t.records = []

        for f in Path(t.path).iterdir():
            if f.is_file():
                
                # for now - csv files are lists, yaml files are rows
                match f.suffix:
                    case ".csv":
                        type = RecordType.list
                    case ".yaml" | ".yml":
                        type = RecordType.row
                    case _:
                        continue

                r = Record(
                    name=f.stem,
                    path=str(f),
                    type=type
                )

                t.records.append(r)

    return tables