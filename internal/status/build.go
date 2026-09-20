package status

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	entitydomain "github.com/apsdsm/joka/internal/domains/entity/domain"
	lockdomain "github.com/apsdsm/joka/internal/domains/lock/domain"
	templateinfra "github.com/apsdsm/joka/internal/domains/template/infra"
	"github.com/apsdsm/joka/internal/meta"
)

// LockReader reads the advisory lock row. It is satisfied by the lock domain's
// LockAdapter, but status only ever calls GetLock.
type LockReader interface {
	GetLock(ctx context.Context) (*lockdomain.Lock, error)
}

// Inputs is everything Build needs: the resolved configuration, the readers for
// each domain's tracking tables, and a Probe for the questions asked of the
// database directly.
type Inputs struct {
	Profile string
	Driver  string

	MigrationsDir string
	EntitiesDir   string
	TemplatesDir  string
	Tables        []templateinfra.TableConfig

	Migration MigrationReader
	Lock      LockReader
	Probe     Probe

	// EntityState is what joka last applied, loaded by the caller. A database
	// with no tracking tables loads as an empty state, which is why the
	// missing-table findings still come from Probe rather than from here.
	EntityState *entitydomain.State

	// Conn is used for the reads that are not behind a domain adapter — the
	// joka_meta marker. Optional: a nil connection reports no marker.
	Conn *sql.DB

	// Now is used to age the lock. Zero means time.Now().
	Now time.Time
}

// Build assembles the report.
//
// A problem confined to one section is recorded in that section's Skipped field
// rather than returned: a database with no migrations table still has entity
// state worth seeing, and the whole point of the command is to render something
// on exactly the databases where other commands give up. An error is returned
// only when the database cannot answer at all.
func Build(ctx context.Context, in Inputs) (Report, error) {
	if in.Now.IsZero() {
		in.Now = time.Now()
	}

	var metaState meta.State
	if in.Conn != nil {
		var err error
		metaState, err = meta.Read(ctx, in.Conn)
		if err != nil {
			return Report{}, err
		}
	}

	report := Report{
		Profile: in.Profile,
		Driver:  in.Driver,
		Meta:    metaState,
	}

	migrations, err := buildMigrations(ctx, in)
	if err != nil {
		return report, err
	}
	report.Migrations = migrations

	entities, err := buildEntities(ctx, in)
	if err != nil {
		return report, err
	}
	report.Entities = entities

	templates, err := buildTemplates(ctx, in)
	if err != nil {
		return report, err
	}
	report.Templates = templates

	lock, err := buildLock(ctx, in)
	if err != nil {
		return report, err
	}
	report.Lock = lock

	report.Actions = deriveActions(report)
	report.normalize()

	return report, nil
}

// buildLock reports the advisory lock if one is held. The lock adapter's
// GetLock creates joka_lock when it is absent, so the read is gated on the
// table already existing to keep status read-only.
func buildLock(ctx context.Context, in Inputs) (*Lock, error) {
	exists, err := in.Probe.TableExists(ctx, "joka_lock")
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}

	held, err := in.Lock.GetLock(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the lock: %w", err)
	}
	if held == nil {
		return nil, nil
	}

	return &Lock{
		LockedBy:  held.LockedBy,
		LockedAt:  held.LockedAt.Format("2006-01-02 15:04:05"),
		Operation: held.Operation,
		Age:       in.Now.Sub(held.LockedAt).Round(time.Second).String(),
	}, nil
}
