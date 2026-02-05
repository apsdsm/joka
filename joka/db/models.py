from datetime import datetime
from pydantic import BaseModel


class MigrationRow(BaseModel):
    """
    Representation of a row in the migrations table.
    """
    id: int
    migration_index: str
    applied_at: datetime
