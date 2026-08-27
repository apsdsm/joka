// Package meta records what joka wrote a database's bookkeeping with.
//
// The `joka_*` tracking tables have changed shape three times without any
// marker saying so: `content_hash` was added to `joka_entities`, `ref_id` and
// `pk_column` to `joka_entity_rows`, and the PostgreSQL snapshot format changed
// in v0.13.0. Each was handled by sniffing — checking whether a column exists,
// or tolerating an empty value — which works only while joka is the newer of
// the two and knows what to look for.
//
// The case that cannot be handled by sniffing is the opposite one: an older
// joka pointed at a database a newer joka has already written. It has no way to
// know the format moved, so it reads the new shape as if it were the old one.
// TrackingVersion exists to make that detectable and refusable.
package meta

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"

	jokadb "github.com/apsdsm/joka/db"
)

// TrackingVersion is the version of the `joka_*` bookkeeping this build writes
// and understands.
//
// Bump it when the meaning or shape of the tracking tables changes in a way an
// older joka would read wrongly — not for additive changes an older joka
// ignores harmlessly. Every bump needs a note in CLAUDE.md saying what moved.
//
//	1 — joka_migrations, joka_snapshots, joka_entities (with content_hash),
//	    joka_entity_rows (with ref_id and pk_column), joka_lock. The state as of
//	    v0.14.0, and the assumed version of any database with tracking tables
//	    but no joka_meta.
//	2 — joka_entity_rows.ref_id is unique and required. _id, not (file,
//	    position), identifies a tracked row; entity_file is metadata recording
//	    where the entity was last declared. internal/upgrade adds the index
//	    after checking the rows allow it.
const TrackingVersion = 2

// PreMarkerVersion is the version of a database that has tracking tables but no
// joka_meta. Such a database was written before the marker existed, so it is at
// the version that existed then — not at whatever is current. Defaulting to the
// current version instead would make every un-upgraded database look upgraded.
const PreMarkerVersion = 1

// Table is the key/value table this package owns.
const Table = "joka_meta"

// Keys written by Stamp.
const (
	// KeyTrackingVersion is TrackingVersion as a decimal string.
	KeyTrackingVersion = "tracking_version"
	// KeyJokaVersion is the joka release that last wrote here. Informational —
	// nothing branches on it, but "which joka last touched this database" is
	// the first question worth asking when something looks wrong.
	KeyJokaVersion = "joka_version"
)

// ErrTrackingVersionTooNew means the database was written by a joka whose
// bookkeeping this build does not understand.
var ErrTrackingVersionTooNew = errors.New("database bookkeeping is newer than this joka")

// State is what the database records about itself. Present is false when
// joka_meta does not exist, which is every database written before v0.14.0.
type State struct {
	Present bool `json:"present"`
	// TrackingVersion is the recorded version, or PreMarkerVersion when absent.
	TrackingVersion int    `json:"tracking_version"`
	JokaVersion     string `json:"joka_version,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
}

// Read returns what the database records. It creates nothing, so it is safe for
// read-only commands.
func Read(ctx context.Context, db *sql.DB) (State, error) {
	state := State{TrackingVersion: PreMarkerVersion}

	exists, err := jokadb.TableExists(ctx, db, Table)
	if err != nil {
		return state, err
	}
	if !exists {
		return state, nil
	}
	state.Present = true

	rows, err := db.QueryContext(ctx, `SELECT key, value, updated_at FROM `+Table)
	if err != nil {
		return state, fmt.Errorf("reading %s: %w", Table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var key, value string
		var updated time.Time
		if err := rows.Scan(&key, &value, &updated); err != nil {
			return state, fmt.Errorf("scanning %s: %w", Table, err)
		}

		switch key {
		case KeyTrackingVersion:
			// An unparseable version is treated as "from the future": joka
			// wrote a decimal string, so anything else came from something
			// this build does not understand.
			n, convErr := strconv.Atoi(value)
			if convErr != nil {
				return state, fmt.Errorf("%w: %s holds %q, which is not a version this joka wrote",
					ErrTrackingVersionTooNew, KeyTrackingVersion, value)
			}
			state.TrackingVersion = n
			state.UpdatedAt = updated.Format("2006-01-02 15:04:05")
		case KeyJokaVersion:
			state.JokaVersion = value
		}
	}

	return state, rows.Err()
}

// Check refuses a database whose bookkeeping is newer than this build
// understands. It reads only, and says nothing about a database that has no
// marker.
func Check(ctx context.Context, db *sql.DB) error {
	state, err := Read(ctx, db)
	if err != nil {
		return err
	}

	if state.TrackingVersion > TrackingVersion {
		by := ""
		if state.JokaVersion != "" {
			by = fmt.Sprintf(" (written by joka %s)", state.JokaVersion)
		}
		return fmt.Errorf("%w: the database is at tracking version %d%s, this joka understands %d — upgrade joka",
			ErrTrackingVersionTooNew, state.TrackingVersion, by, TrackingVersion)
	}

	return nil
}

// Stamp records the current tracking version and joka version, creating
// joka_meta if it is absent. Called by mutating commands only: a read-only
// command must leave a bare database bare.
func Stamp(ctx context.Context, db *sql.DB, jokaVersion string) error {
	if err := ensureTable(ctx, db); err != nil {
		return err
	}

	for key, value := range map[string]string{
		KeyTrackingVersion: strconv.Itoa(TrackingVersion),
		KeyJokaVersion:     jokaVersion,
	} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO `+Table+` (key, value, updated_at)
			VALUES ($1, $2, CURRENT_TIMESTAMP)
			ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = CURRENT_TIMESTAMP
		`, key, value); err != nil {
			return fmt.Errorf("recording %s: %w", key, err)
		}
	}

	return nil
}

func ensureTable(ctx context.Context, db *sql.DB) error {
	exists, err := jokadb.TableExists(ctx, db, Table)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	_, err = db.ExecContext(ctx, `
		CREATE TABLE `+Table+` (
			key VARCHAR(64) PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)
	`)
	if err != nil {
		return fmt.Errorf("creating %s: %w", Table, err)
	}
	return nil
}
