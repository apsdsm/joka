from typing import List
from pydantic import BaseModel

from joka.models.table import Table

class Schema(BaseModel):
    """
    Representation of the state data configuration schema.
    """

    name: str  # Name of the schema
    tables: List[Table]  # List of tables in the schema 

