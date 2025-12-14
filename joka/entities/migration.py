from dataclasses import dataclass

# statuses for applied migrations
STATUS_APPLIED = "applied"
STATUS_PENDING = "pending"
STATUS_OUT_OF_ORDER = "out_of_order"
STATUS_FILE_MISSING = "file_missing"

@dataclass
class Migration:
    """
    Representation of an applied migration.
    """
    id: int 
    migration_index: str
    applied_at: str  # ISO formatted datetime string
    file_name: str
    file_full_path: str
    status: str  # e.g., "applied", "pending"