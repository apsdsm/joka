// Package upgrade moves a database's joka_* bookkeeping forward to the version
// this build writes.
//
// One place records what each version bump does, because the alternative —
// format changes absorbed by sniffing, scattered across whichever adapter
// happened to notice — is how joka ended up with three undocumented format
// changes and no way to tell them apart.
//
// An upgrade runs automatically, but only when it is safe: each step checks the
// data first and refuses with a report of exactly what stands in the way. The
// common case is invisible; the case that needs a human is loud. Nothing here
// deletes or rewrites application data — a step may add a constraint or a
// column, never change what a row means.
package upgrade

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/domains/entity/infra"
	"github.com/apsdsm/joka/internal/meta"
)

// ErrBlocked means an upgrade cannot run until something in the data is
// resolved. The error names what.
var ErrBlocked = errors.New("tracking upgrade is blocked")

// Step is one version bump.
type Step struct {
	// To is the tracking version this step reaches.
	To int
	// Describe says what the step does, for the line printed when it runs.
	Describe string
	// Blockers reports what stands in the way, empty when the step can run.
	Blockers func(ctx context.Context, db *sql.DB) ([]string, error)
	// Apply performs the change. It must be idempotent.
	Apply func(ctx context.Context, db *sql.DB) error
}

// Steps are applied in order for any database below meta.TrackingVersion.
var Steps = []Step{
	{
		To:       2,
		Describe: "make _id the identity of a tracked row (unique index on joka_entity_rows.ref_id)",
		Blockers: refIDBlockers,
		Apply: func(ctx context.Context, db *sql.DB) error {
			exists, err := jokadb.TableExists(ctx, db, "joka_entity_rows")
			if err != nil || !exists {
				// Nothing has been synced here; the table will be created with
				// the index when something is.
				return err
			}
			return infra.NewPostgresDBAdapter(db).EnsureRefIDIndex(ctx)
		},
	},
}

// Run brings the database up to meta.TrackingVersion and returns the steps it
// applied. A database already at the current version is left alone.
func Run(ctx context.Context, db *sql.DB, jokaVersion string) ([]Step, error) {
	state, err := meta.Read(ctx, db)
	if err != nil {
		return nil, err
	}

	var applied []Step

	for _, step := range Steps {
		if step.To <= state.TrackingVersion {
			continue
		}

		blockers, err := step.Blockers(ctx, db)
		if err != nil {
			return applied, err
		}
		if len(blockers) > 0 {
			return applied, fmt.Errorf("%w: cannot %s\n  %s",
				ErrBlocked, step.Describe, strings.Join(blockers, "\n  "))
		}

		if err := step.Apply(ctx, db); err != nil {
			return applied, fmt.Errorf("upgrading tracking to version %d: %w", step.To, err)
		}

		applied = append(applied, step)
	}

	if len(applied) > 0 || !state.Present {
		if err := meta.Stamp(ctx, db, jokaVersion); err != nil {
			return applied, err
		}
	}

	return applied, nil
}

// maxReported caps how many offending rows an upgrade lists. Enough to see the
// shape of the problem without a screen of it.
const maxReported = 10

// refIDBlockers reports what stops ref_id becoming unique and required: rows
// tracked before _id was recorded, and _ids claimed by more than one row.
//
// The duplicate case is real rather than theoretical: two entity sets seeded
// into one database (a dev1/ tree and a local/ tree of the same seeds) both
// claim the same _ids, which version 1 allowed because it keyed on the file.
func refIDBlockers(ctx context.Context, db *sql.DB) ([]string, error) {
	exists, err := jokadb.TableExists(ctx, db, "joka_entity_rows")
	if err != nil || !exists {
		return nil, err
	}

	var blockers []string

	empty, err := countRows(ctx, db,
		`SELECT count(*) FROM joka_entity_rows WHERE ref_id IS NULL OR ref_id = ''`)
	if err != nil {
		return nil, err
	}
	if empty > 0 {
		files, err := listStrings(ctx, db, `
			SELECT DISTINCT entity_file FROM joka_entity_rows
			WHERE ref_id IS NULL OR ref_id = ''
			ORDER BY entity_file LIMIT $1`, maxReported)
		if err != nil {
			return nil, err
		}
		blockers = append(blockers, fmt.Sprintf(
			"%s no _id, from: %s — synced before joka recorded one. "+
				"Re-sync with 'joka entity reimport <file>', or drop their tracking with "+
				"'joka entity forget <file>'", trackedRows(empty), strings.Join(files, ", ")))
	}

	dupes, err := listStrings(ctx, db, `
		SELECT ref_id || ' (' || count(*) || ' rows, in ' || string_agg(DISTINCT entity_file, ' and ') || ')'
		FROM joka_entity_rows
		WHERE ref_id IS NOT NULL AND ref_id <> ''
		GROUP BY ref_id HAVING count(*) > 1
		ORDER BY ref_id LIMIT $1`, maxReported)
	if err != nil {
		return nil, err
	}
	if len(dupes) > 0 {
		blockers = append(blockers, fmt.Sprintf(
			"these _ids are claimed by more than one tracked row: %s. An _id identifies one row, so "+
				"one claim has to go — 'joka entity forget <file>' drops a file's tracking without "+
				"touching its rows", strings.Join(dupes, "; ")))
	}

	return blockers, nil
}

func countRows(ctx context.Context, db *sql.DB, query string) (int, error) {
	var n int
	if err := db.QueryRowContext(ctx, query).Scan(&n); err != nil {
		return 0, fmt.Errorf("checking tracked rows: %w", err)
	}
	return n, nil
}

func listStrings(ctx context.Context, db *sql.DB, query string, args ...any) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing tracked rows: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("scanning tracked rows: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// trackedRows renders a count of tracked rows with the right verb, so the
// blocker reads as a sentence.
func trackedRows(n int) string {
	if n == 1 {
		return "1 tracked row has"
	}
	return fmt.Sprintf("%d tracked rows have", n)
}
