// Package status builds a single read-only report of the three states joka
// works between:
//
//   - declared — what the devops folder says: migration files, entity YAML,
//     template records
//   - tracked  — what joka's own tables record: joka_migrations,
//     joka_snapshots, joka_entities, joka_entity_rows
//   - live     — what the database actually contains: tables, columns, rows
//
// Every mismatch joka can hit is a disagreement between two of those. The
// existing commands each report one edge (migrate status: declared vs tracked;
// migrate verify: tracked vs live; entity status: declared vs tracked) in their
// own format. This package reports all of them at once, and reports the edges
// no command covers today: whether the rows joka tracks for an entity file are
// still in the database, and whether template files and their tables agree.
//
// Nothing here writes. Unlike the other commands, status does not auto-create
// the joka_* tracking tables — a missing table is a finding, not something to
// fix on the way past.
package status

import (
	entityapp "github.com/apsdsm/joka/internal/domains/entity/app"
	"github.com/apsdsm/joka/internal/meta"
)

// Report is the whole picture for one database.
type Report struct {
	Profile string `json:"profile,omitempty"`
	// Meta is what the database records about the joka that last wrote it.
	Meta       meta.State `json:"meta"`
	Driver     string     `json:"driver"`
	Migrations Migrations `json:"migrations"`
	Entities   Entities   `json:"entities"`
	Templates  Templates  `json:"templates"`
	Lock       *Lock      `json:"lock"`
	Actions    []Action   `json:"actions"`
}

// Migration status values. These describe the declared-vs-tracked edge for a
// single migration and are deliberately a superset of the migration domain's
// own constants: status aligns by index rather than by position, so it can
// report a chain that GetMigrationChainAction refuses to build at all.
const (
	MigrationApplied     = "applied"
	MigrationPending     = "pending"
	MigrationOutOfOrder  = "out_of_order"
	MigrationFileMissing = "file_missing"
)

// Migrations is the migration section: one row per migration index known to
// either the directory or the tracking table, plus the schema drift check.
type Migrations struct {
	Dir string `json:"dir"`
	// Skipped explains why the section has no rows (no migrations table, dir
	// unreadable). Empty when the section was built.
	Skipped     string      `json:"skipped,omitempty"`
	Files       []Migration `json:"files"`
	Applied     int         `json:"applied"`
	Pending     int         `json:"pending"`
	OutOfOrder  int         `json:"out_of_order"`
	FileMissing int         `json:"file_missing"`
	Drift       Drift       `json:"drift"`
}

// Migration is one migration index across the declared and tracked planes.
type Migration struct {
	Index     string `json:"index"`
	Name      string `json:"name,omitempty"`
	Declared  bool   `json:"declared"` // a file with this index is on disk
	Tracked   bool   `json:"tracked"`  // a row with this index is in joka_migrations
	AppliedAt string `json:"applied_at,omitempty"`
	Status    string `json:"status"`
}

// Drift is the tracked-vs-live edge for the schema: the snapshot captured for
// the most recent migration compared against the tables that are really there.
type Drift struct {
	Checked bool `json:"checked"`
	// Skipped explains why no comparison was made (no snapshots table, no
	// snapshot captured yet).
	Skipped       string          `json:"skipped,omitempty"`
	SnapshotIndex string          `json:"snapshot_index,omitempty"`
	Added         []string        `json:"added"`    // in live, not in the snapshot
	Removed       []string        `json:"removed"`  // in the snapshot, not in live
	Modified      []ModifiedTable `json:"modified"` // in both, CREATE statements differ
}

// HasDrift reports whether the comparison found any difference.
func (d Drift) HasDrift() bool {
	return len(d.Added) > 0 || len(d.Removed) > 0 || len(d.Modified) > 0
}

// Count returns the number of tables that differ.
func (d Drift) Count() int {
	return len(d.Added) + len(d.Removed) + len(d.Modified)
}

// ModifiedTable is a table whose live CREATE statement differs from the
// snapshot. Snapshot and Live carry the full statements for programmatic use;
// OnlyInLive and OnlyInSnapshot are the differing lines, which is what the
// text report shows. `migrate verify` prints the full statements.
type ModifiedTable struct {
	Table          string   `json:"table"`
	Snapshot       string   `json:"snapshot"`
	Live           string   `json:"live"`
	OnlyInLive     []string `json:"only_in_live"`
	OnlyInSnapshot []string `json:"only_in_snapshot"`
}

// Entities is the entity section: one row per entity file on disk or tracked
// in joka_entities.
type Entities struct {
	Dir     string       `json:"dir"`
	Skipped string       `json:"skipped,omitempty"`
	Files   []EntityFile `json:"files"`
	Counts  Counts       `json:"counts"`
}

// Counts totals the entity files by status.
type Counts struct {
	Synced   int `json:"synced"`
	Modified int `json:"modified"`
	New      int `json:"new"`
	Orphaned int `json:"orphaned"`
}

// EntityFile is one entity YAML file across all three planes: the entities it
// declares, the rows joka tracks for it, and whether those rows are still in
// the database.
type EntityFile struct {
	Path   string `json:"path"`
	Status string `json:"status"` // synced | modified | new | orphaned
	// Declared is the number of entities in the file, flattened depth-first —
	// the same sequence entity sync inserts and tracks. -1 when the file is
	// not on disk (orphaned) or could not be parsed.
	Declared int `json:"declared"`
	// Tracked is the number of rows in joka_entity_rows for this file.
	Tracked int `json:"tracked"`
	// Live is how many of those tracked rows are still in the database, and
	// MissingRows how many are gone. Rows in a table that no longer exists at
	// all are counted as missing and the table is named in MissingTables.
	Live          int      `json:"live"`
	MissingRows   int      `json:"missing_rows"`
	MissingTables []string `json:"missing_tables"`
	// Structural is the reason `entity sync` would refuse to update this file
	// in place, verbatim from the same check sync itself runs. Empty when sync
	// would proceed.
	Structural string `json:"structural,omitempty"`
	// KeyedByID reports whether every declared entity and every tracked row
	// carries an _id, which is what an identity-keyed match would need. Always
	// false when the file is not on disk, since there is nothing to check
	// against.
	KeyedByID  bool   `json:"keyed_by_id"`
	ParseError string `json:"parse_error,omitempty"`
	// IdentityProblems are the entities in this file with no _id, or whose _id
	// another file also claims. Empty when the file is ready for identity-keyed
	// tracking.
	IdentityProblems []entityapp.EntitySetProblem `json:"identity_problems"`
}

// Templates is the template section: one row per table declared in the
// .jokarc.yaml `tables:` list.
type Templates struct {
	Dir     string          `json:"dir"`
	Skipped string          `json:"skipped,omitempty"`
	Tables  []TemplateTable `json:"tables"`
}

// TemplateTable compares the rows a table's record files declare against the
// rows the table actually holds. Note that only the `truncate` strategy makes
// the two exactly equal; `update` tables can legitimately hold more.
type TemplateTable struct {
	Name     string `json:"name"`
	Strategy string `json:"strategy"`
	Files    int    `json:"files"`
	Declared int    `json:"declared"`
	// Live is the row count in the table, or -1 when the table does not exist.
	Live         int    `json:"live"`
	TableMissing bool   `json:"table_missing"`
	LoadError    string `json:"load_error,omitempty"`
}

// Lock is the joka_lock row, when one is held.
type Lock struct {
	LockedBy  string `json:"locked_by"`
	LockedAt  string `json:"locked_at"`
	Operation string `json:"operation"`
	Age       string `json:"age"`
}

// Action scopes.
const (
	ScopeMigrations = "migrations"
	ScopeEntities   = "entities"
	ScopeTemplates  = "templates"
	ScopeLock       = "lock"
)

// Action is one thing a person can do about a mismatch the report found.
// Command is empty when joka has no command that resolves it — the orphaned
// entity file being the current example.
type Action struct {
	Scope   string `json:"scope"`
	Subject string `json:"subject,omitempty"`
	Reason  string `json:"reason"`
	Command string `json:"command,omitempty"`
}

// normalize replaces nil slices with empty ones. JSON consumers get [] rather
// than null for "nothing found", so `.length` and iteration work without a
// null check on every field.
func (r *Report) normalize() {
	if r.Migrations.Files == nil {
		r.Migrations.Files = []Migration{}
	}
	if r.Migrations.Drift.Added == nil {
		r.Migrations.Drift.Added = []string{}
	}
	if r.Migrations.Drift.Removed == nil {
		r.Migrations.Drift.Removed = []string{}
	}
	if r.Migrations.Drift.Modified == nil {
		r.Migrations.Drift.Modified = []ModifiedTable{}
	}
	for i := range r.Migrations.Drift.Modified {
		if r.Migrations.Drift.Modified[i].OnlyInLive == nil {
			r.Migrations.Drift.Modified[i].OnlyInLive = []string{}
		}
		if r.Migrations.Drift.Modified[i].OnlyInSnapshot == nil {
			r.Migrations.Drift.Modified[i].OnlyInSnapshot = []string{}
		}
	}
	if r.Entities.Files == nil {
		r.Entities.Files = []EntityFile{}
	}
	for i := range r.Entities.Files {
		if r.Entities.Files[i].IdentityProblems == nil {
			r.Entities.Files[i].IdentityProblems = []entityapp.EntitySetProblem{}
		}
		if r.Entities.Files[i].MissingTables == nil {
			r.Entities.Files[i].MissingTables = []string{}
		}
	}
	if r.Templates.Tables == nil {
		r.Templates.Tables = []TemplateTable{}
	}
	if r.Actions == nil {
		r.Actions = []Action{}
	}
}
