from dataclasses import dataclass
from datetime import datetime
from pydantic import BaseModel

class MigrationModel (BaseModel):
    """
    Representation of a database migration.
    """
    id: int
    migration_index: str
    applied_at: datetime