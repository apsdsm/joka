// Package status builds a read-only inventory of what joka has done to one
// database.
//
// It is an inventory, not a diff. `joka plan` answers "what would change";
// status answers "what is". That line is what stops the two being two names for
// one thing, and it is the same split terraform draws between plan and show.
//
// Most of what it reports had nowhere to be said. The state audit computes seven
// verdicts and only one of them was ever acted on — the other six were
// discarded, and their sentences had no caller outside the test suite. The
// tracking marker was written and never read back. A held lock was discoverable
// only by trying a mutating command and being refused.
//
// **It creates nothing.** Every other command auto-creates the table it needs;
// status must not, because a missing table is a finding rather than something to
// fix on the way past. Two reads would create one if called directly
// (`GetLatestSnapshotIndex` ensures joka_snapshots, the lock adapter's `GetLock`
// ensures joka_lock), so both are gated behind a table probe.
package status

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	jokadb "github.com/apsdsm/joka/db"
	entityapp "github.com/apsdsm/joka/internal/domains/entity/app"
	entitydomain "github.com/apsdsm/joka/internal/domains/entity/domain"
	entityinfra "github.com/apsdsm/joka/internal/domains/entity/infra"
	lockdomain "github.com/apsdsm/joka/internal/domains/lock/domain"
	lockinfra "github.com/apsdsm/joka/internal/domains/lock/infra"
	migrationapp "github.com/apsdsm/joka/internal/domains/migration/app"
	migrationinfra "github.com/apsdsm/joka/internal/domains/migration/infra"
	"github.com/apsdsm/joka/internal/meta"
)

// Inputs is what the command hands the builder.
type Inputs struct {
	DB            *sql.DB
	Profile       string
	MigrationsDir string
	EntitiesDirs  []string
	StateFile     string
}

// Report is the whole inventory. Text and JSON render the same value.
type Report struct {
	Profile string     `json:"profile,omitempty"`
	Meta    meta.State `json:"meta"`

	StateFile  StateFile  `json:"state_file"`
	Migrations Migrations `json:"migrations"`
	Entities   Entities   `json:"entities"`

	// Lock is nil when nothing holds it.
	Lock *Lock `json:"lock"`
}

// StateFile is the audit copy beside the working directory, and what comparing
// it against the database's own markers concluded.
type StateFile struct {
	Path    string `json:"path"`
	Present bool   `json:"present"`
	// Verdict is one of app.StateAudit. Note is its sentence.
	Verdict string `json:"verdict"`
	Note    string `json:"note"`
	// NeedsAttention is false for the two verdicts that mean "normal".
	NeedsAttention bool `json:"needs_attention"`
}

// Migrations is the schema half.
type Migrations struct {
	// Tracked is false when joka_migrations does not exist, which is a database
	// that has never had `joka init` run.
	Tracked bool `json:"tracked"`
	Applied int  `json:"applied"`
	Pending int  `json:"pending"`
	// Latest is the index of the last applied migration.
	Latest string `json:"latest,omitempty"`
	// Snapshots counts the stored schema snapshots, and LatestSnapshot names the
	// migration the most recent one was captured at. This is what `migrate
	// snapshot` used to be reached for; the statements themselves are a debugging
	// detail, not a status line.
	Snapshots      int    `json:"snapshots"`
	LatestSnapshot string `json:"latest_snapshot,omitempty"`
	// Drift counts the tables whose live shape disagrees with the snapshot.
	// Checked counts as false when there was no snapshot to compare against.
	DriftChecked bool `json:"drift_checked"`
	Drift        int  `json:"drift"`
	// Problem is set when the chain could not be read at all — a broken chain, a
	// missing directory. The rest of the section is then unreliable and says so.
	Problem string `json:"problem,omitempty"`
}

// Entities is the seed half.
type Entities struct {
	Dir string `json:"dir"`
	// Tracked is how many _ids joka has written here, Files how many seed files
	// it has recorded, Unkeyed how many rows it carries with no _id.
	Tracked int `json:"tracked"`
	Files   int `json:"files"`
	Unkeyed int `json:"unkeyed"`
	// Declared is how many entities the files on disk declare. It is 0 with
	// Problem set when they could not be read.
	Declared int    `json:"declared"`
	Problem  string `json:"problem,omitempty"`
}

// Lock is the visibility row, when one is there.
type Lock struct {
	LockedBy  string `json:"locked_by"`
	LockedAt  string `json:"locked_at"`
	Operation string `json:"operation"`
}

// Build assembles the report. It never writes and never creates a table.
//
// A section that cannot be read sets its own Problem and the rest still builds:
// the reason to run status is usually that something is wrong, so one unreadable
// half must not take the other with it.
func Build(ctx context.Context, in Inputs) (*Report, error) {
	report := &Report{Profile: in.Profile}

	markers, err := meta.Read(ctx, in.DB)
	if err != nil {
		return nil, err
	}
	report.Meta = markers

	report.StateFile = buildStateFile(in, markers)

	state, err := entityinfra.NewPostgresStateBackend(in.DB).Load(ctx)
	if err != nil {
		// A state that cannot be read is itself the finding — a duplicate _id
		// blocking the version 3 upgrade reads exactly this way.
		state = entitydomain.NewState()
		report.Entities.Problem = err.Error()
	}

	report.Migrations = buildMigrations(ctx, in)
	buildEntities(ctx, in, state, &report.Entities)

	lock, err := readLock(ctx, in.DB)
	if err != nil {
		return nil, err
	}
	report.Lock = lock

	return report, nil
}

func buildStateFile(in Inputs, markers meta.State) StateFile {
	path := entityinfra.StateFilePath(in.StateFile, in.Profile)

	doc, present, err := entityinfra.ReadStateFile(path)
	if err != nil {
		return StateFile{Path: path, Verdict: "unreadable", Note: err.Error(), NeedsAttention: true}
	}

	audit := entityapp.AuditState(present, doc.Identity, doc.Version,
		markers.StateIdentity, markers.StateVersion)

	return StateFile{
		Path:           path,
		Present:        present,
		Verdict:        string(audit),
		Note:           audit.Describe(),
		NeedsAttention: audit.NeedsAttention(),
	}
}

func buildMigrations(ctx context.Context, in Inputs) Migrations {
	out := Migrations{}

	tracked, err := jokadb.TableExists(ctx, in.DB, "joka_migrations")
	if err != nil {
		return Migrations{Problem: err.Error()}
	}
	out.Tracked = tracked
	if !tracked {
		return out
	}

	adapter := migrationinfra.NewPostgresDBAdapter(in.DB)

	chain, err := (migrationapp.GetMigrationChainAction{
		DB: adapter, MigrationsDir: in.MigrationsDir,
	}).Execute(ctx)
	if err != nil {
		out.Problem = err.Error()
		return out
	}

	for _, m := range chain {
		switch m.Status {
		case entitydomainStatusApplied:
			out.Applied++
			out.Latest = m.MigrationIndex
		case entitydomainStatusPending:
			out.Pending++
		}
	}

	addSnapshots(ctx, in, adapter, &out)
	return out
}

// The migration package's status constants, spelled once here so the switch
// above reads as a switch rather than as two string literals.
const (
	entitydomainStatusApplied = "applied"
	entitydomainStatusPending = "pending"
)

// addSnapshots fills in the snapshot count and the drift check, both gated on
// the table existing: GetLatestSnapshotIndex would create it otherwise, and
// status creates nothing.
func addSnapshots(ctx context.Context, in Inputs, adapter *migrationinfra.PostgresDBAdapter, out *Migrations) {
	hasSnapshots, err := jokadb.TableExists(ctx, in.DB, "joka_snapshots")
	if err != nil || !hasSnapshots {
		return
	}

	if err := in.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM joka_snapshots`).Scan(&out.Snapshots); err != nil {
		return
	}
	if out.Snapshots == 0 {
		return
	}

	index, err := adapter.GetLatestSnapshotIndex(ctx)
	if err != nil {
		return
	}
	out.LatestSnapshot = index

	result, err := (migrationapp.VerifySchemaAction{DB: adapter}).Execute(ctx)
	if err != nil {
		return
	}
	out.DriftChecked = true
	out.Drift = len(result.Added) + len(result.Removed) + len(result.Modified)
}

func buildEntities(ctx context.Context, in Inputs, state *entitydomain.State, out *Entities) {
	out.Dir = strings.Join(in.EntitiesDirs, ", ")
	out.Tracked = len(state.Entities)
	out.Files = len(state.Files)
	out.Unkeyed = len(state.Unkeyed)

	found, err := entityinfra.DiscoverEntityRoots(in.EntitiesDirs)
	if err != nil {
		if out.Problem == "" {
			out.Problem = err.Error()
		}
		return
	}

	for _, disc := range found {
		file, err := entityapp.ParseEntityAction{Path: disc.Full}.Execute()
		if err != nil {
			if out.Problem == "" {
				out.Problem = err.Error()
			}
			return
		}
		out.Declared += entityapp.CountEntities(file.Entities)
	}
	_ = ctx
}

// readLock reports the visibility row without creating the table it lives in.
func readLock(ctx context.Context, db *sql.DB) (*Lock, error) {
	exists, err := jokadb.TableExists(ctx, db, "joka_lock")
	if err != nil || !exists {
		return nil, err
	}

	held, err := lockinfra.NewPostgresLockAdapter(db).GetLock(ctx)
	if err != nil {
		if errors.Is(err, lockdomain.ErrLockHeld) {
			return nil, nil
		}
		return nil, err
	}
	if held == nil {
		return nil, nil
	}

	return &Lock{
		LockedBy:  held.LockedBy,
		LockedAt:  held.LockedAt.Format("2006-01-02 15:04:05"),
		Operation: held.Operation,
	}, nil
}
