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
	// Nil when nothing can stand in the way — a step that only removes a
	// constraint, or does nothing at all, cannot be refused by the data.
	Blockers func(ctx context.Context, db *sql.DB) ([]string, error)
	// Apply performs the change. It must be idempotent.
	Apply func(ctx context.Context, db *sql.DB) error
}

// Steps are applied in order for any database below meta.TrackingVersion.
var Steps = []Step{
	{
		To:       2,
		Describe: "make _id the identity of a tracked row",
		Apply: func(context.Context, *sql.DB) error {
			// Nothing. This step added a unique index to joka_entity_rows, and
			// version 3 drops that table — a database going 1 → 3 would create
			// the index and delete it in the same run.
			//
			// It is a no-op rather than deleted, because removing a step
			// renumbers history and the version marker exists to stop exactly
			// that. Its blockers are gone too, and that is the point: they
			// refused a database with rows that had no _id, and named
			// `entity reimport` and `entity forget` as the remedy — both of
			// which the same refusal blocked, along with `drop` and `reset`.
			// A database in that state had no joka command that could move it.
			// Version 3 carries those rows into the document's Unkeyed instead,
			// where `joka status` reports them and `entity forget` clears them.
			return nil
		},
	},
	{
		To:       3,
		Describe: "move entity tracking into one joka_state document",
		Blockers: ambiguousIDBlockers,
		Apply:    migrateEntityStateToDocument,
	},
}

// migrateEntityStateToDocument reads the decomposed entity tracking and writes
// it back as one document, then drops the tables it came from.
//
// It is idempotent by construction: the backend's Load prefers the document, so
// a retry after a partial run reads what was already written rather than the
// tables it is in the middle of replacing.
func migrateEntityStateToDocument(ctx context.Context, db *sql.DB) error {
	backend := infra.NewPostgresStateBackend(db)

	if err := backend.EnsureStateTable(ctx); err != nil {
		return err
	}

	state, err := backend.Load(ctx)
	if err != nil {
		return err
	}
	if err := backend.Save(ctx, state); err != nil {
		return err
	}

	// Leaving them would be a second copy of what the document now holds, which
	// is the failure this whole change is undoing. Row tracking goes first: it
	// is the one with the foreign-key-free dependency on the other.
	for _, table := range []string{"joka_entity_rows", "joka_entities"} {
		if _, err := db.ExecContext(ctx, `DROP TABLE IF EXISTS `+table); err != nil {
			return fmt.Errorf("dropping %s after moving it into the document: %w", table, err)
		}
	}

	return nil
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

		if step.Blockers != nil {
			blockers, err := step.Blockers(ctx, db)
			if err != nil {
				return applied, err
			}
			if len(blockers) > 0 {
				return applied, fmt.Errorf("%w: cannot %s\n  %s",
					ErrBlocked, step.Describe, strings.Join(blockers, "\n  "))
			}
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

// ambiguousIDBlockers reports the one thing the state document genuinely cannot
// represent: an _id claimed by more than one tracked row. The document is a map
// keyed on _id, so two rows under one key is not a hard case, it is an
// impossible one.
//
// It is real rather than theoretical: two entity sets seeded into one database
// (a dev1/ tree and a local/ tree of the same seeds) both claim the same _ids,
// which version 1 allowed because it keyed on the file.
//
// A row with *no* _id is deliberately not a blocker. It used to be, and the
// refusal named `entity reimport` and `entity forget` as the remedy — both of
// which the same refusal blocked, along with `drop` and `reset`. A database in
// that state had no joka command that could move it. Those rows are carried
// into the document's Unkeyed instead, where `joka status` reports them and
// `entity forget` clears them.
func ambiguousIDBlockers(ctx context.Context, db *sql.DB) ([]string, error) {
	exists, err := jokadb.TableExists(ctx, db, "joka_entity_rows")
	if err != nil || !exists {
		return nil, err
	}

	var blockers []string

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
				"one claim has to go. No joka command can do it: every one that writes is gated on "+
				"this upgrade, and reading the tracking fails on the same ambiguity. Drop the losing "+
				"claim directly, e.g. DELETE FROM joka_entity_rows WHERE entity_file = '<file>'",
			strings.Join(dupes, "; ")))
	}

	return blockers, nil
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
