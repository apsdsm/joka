package domain

// Migration status constants used to classify each migration in the chain.
const (
	StatusApplied     = "applied"
	StatusPending     = "pending"
	StatusOutOfOrder  = "out_of_order"
	StatusFileMissing = "file_missing"
)

// Migration is the aggregate that combines database state and file state for
// a single migration, along with a computed status indicating whether it has
// been applied, is pending, or has a problem.
type Migration struct {
	ID             int
	MigrationIndex string
	AppliedAt      string // ISO formatted datetime string, empty if pending
	FileName       string
	FileFullPath   string
	Status         string // one of the Status* constants
}
