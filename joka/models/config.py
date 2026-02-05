from dataclasses import dataclass
from typing import List

from joka.models.schema import Schema

@dataclass
class Config:
    """
    Representation of the state data configuration.
    """
    
    schemas: List[Schema]  # List of schemas in the configuration
    