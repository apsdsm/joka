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
//	3 — entity tracking is one jsonb document in joka_state, keyed 'entities'.
//	    joka_entities and joka_entity_rows are dropped. An older joka reading
//	    this database would find no tracking at all and re-insert every seeded
//	    row, which is what makes the bump mandatory rather than additive.
const TrackingVersion = 3

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

	// KeyStateIdentity is a UUID naming this database, stamped once and never
	// rewritten. It is what a state file on disk is checked against: the file
	// carries the identity of the database it describes, so pointing it at
	// another one is detectable rather than silently wrong.
	//
	// It travels with a dump, which is the point — a restored database is the
	// same database, at an earlier version.
	KeyStateIdentity = "state_identity"

	// KeyStateRoot names the joka root that owns this database — the value of
	// `root:` in the configuration the owning directory holds.
	//
	// It exists because state_identity could not carry this. That identity
	// lives in joka_meta and is compared against a copy in the state file, and
	// the state file is generally gitignored — so in CI there is no file, the
	// audit returns no_file, and nothing refuses. Two roots then sync one
	// database and each deletes the other's entities as declared nowhere,
	// under --auto, without a word. That is how jjc2's CI lost 16 rows.
	//
	// The root is declared in a committed file instead, so the comparison
	// survives a checkout with no state file in it. It doubles as the
	// environment label: in a devops/joka/{local,test,prod} layout the root
	// name is the environment, and a prod root pointed at a test database is
	// the same disagreement.
	KeyStateRoot = "state_root"

	// KeyStateVersion counts the times joka has written the entity state,
	// incremented in the same transaction as the write.
	//
	// State inside the database it describes is always self-consistent, so it
	// can never report that the database is the wrong one or an older copy of
	// the right one. This counter is the other half of that comparison: a
	// state file that says 17 against a database that says 12 is a database
	// that was restored or rolled back.
	KeyStateVersion = "state_version"
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

	// StateIdentity names this database; empty before joka has written state
	// here. StateVersion counts the writes.
	StateIdentity string `json:"state_identity,omitempty"`
	StateVersion  int    `json:"state_version,omitempty"`

	// StateRoot is the joka root that claimed this database; empty when no
	// root has, which is every database until a configuration declares one.
	StateRoot string `json:"state_root,omitempty"`
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
		// A database with no joka table at all is a new one, not an old one.
		// PreMarkerVersion says "written before the marker existed", and
		// nothing has written here, so there is no bookkeeping to be behind.
		// Reading it as pre-marker made every fresh database announce the two
		// tracking upgrades on its first mutating command — upgrades of
		// entity tracking that did not exist, applied as no-ops, reported as
		// though something had moved.
		bare, err := isBare(ctx, db)
		if err != nil {
			return state, err
		}
		if bare {
			state.TrackingVersion = TrackingVersion
		}
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
		case KeyStateIdentity:
			state.StateIdentity = value
		case KeyStateRoot:
			state.StateRoot = value
		case KeyStateVersion:
			// Unparseable is treated as zero rather than as an error: the
			// counter is for comparing against a file, and refusing to read
			// the whole marker table over it would take out commands that do
			// not care.
			n, convErr := strconv.Atoi(value)
			if convErr == nil {
				state.StateVersion = n
			}
		}
	}

	return state, rows.Err()
}

// StampStateWrite records that the entity state was written: it assigns an
// identity if the database does not have one yet, and returns the version it
// advanced to.
//
// It takes a DBTX rather than a *sql.DB because it belongs in the transaction
// that wrote the state. A version that could commit without the write it counts
// would be worse than no counter at all.
func StampStateWrite(ctx context.Context, tx DBTX, identity string) (int, error) {
	var current int
	err := tx.QueryRowContext(ctx,
		`SELECT value FROM `+Table+` WHERE key = $1`, KeyStateVersion).Scan(&current)
	if err != nil && err != sql.ErrNoRows {
		return 0, fmt.Errorf("reading %s: %w", KeyStateVersion, err)
	}

	next := current + 1

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO `+Table+` (key, value, updated_at)
		VALUES ($1, $2, CURRENT_TIMESTAMP)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = CURRENT_TIMESTAMP
	`, KeyStateVersion, strconv.Itoa(next)); err != nil {
		return 0, fmt.Errorf("recording %s: %w", KeyStateVersion, err)
	}

	// DO NOTHING, not DO UPDATE: an identity is assigned once and never
	// rewritten, or a state file could never be checked against it.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO `+Table+` (key, value, updated_at)
		VALUES ($1, $2, CURRENT_TIMESTAMP)
		ON CONFLICT (key) DO NOTHING
	`, KeyStateIdentity, identity); err != nil {
		return 0, fmt.Errorf("recording %s: %w", KeyStateIdentity, err)
	}

	return next, nil
}

// DBTX is the subset of *sql.DB and *sql.Tx this package writes through.
type DBTX interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
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
	if err := EnsureTable(ctx, db); err != nil {
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

// EnsureTable creates joka_meta if it is absent. Exported because the entity
// state backend counts its writes there, so the table has to exist by the time
// a command starts writing.
func EnsureTable(ctx context.Context, db *sql.DB) error {
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

// preMarkerTables are the joka tables a build older than joka_meta could have
// left behind. A database holding none of them has no bookkeeping to upgrade.
//
// joka_lock is not among them: it is a visibility row for a lock that is
// released at the end of every run, so it says nothing about what wrote here.
var preMarkerTables = []string{
	"joka_migrations",
	"joka_snapshots",
	"joka_entities",
	"joka_entity_rows",
	"joka_state",
}

// isBare reports whether a database with no joka_meta also has no tracking to
// be behind — a database joka has never written to.
func isBare(ctx context.Context, db *sql.DB) (bool, error) {
	for _, table := range preMarkerTables {
		exists, err := jokadb.TableExists(ctx, db, table)
		if err != nil {
			return false, err
		}
		if exists {
			return false, nil
		}
	}

	return true, nil
}

// ErrWrongRoot means the database is owned by a joka root other than the one
// running, so writing to it would converge it against the wrong desired state.
var ErrWrongRoot = errors.New("this database belongs to a different joka root")

// CheckRoot decides whether a root may write to this database.
//
// Existence has a truth table and so does ownership:
//
//	| recorded | declared |                                              |
//	|----------|----------|----------------------------------------------|
//	| absent   | absent   | nothing; joka worked this way before roots   |
//	| absent   | declared | the root claims it on the way out            |
//	| present  | absent   | refuse — a claimed database must be named    |
//	| present  | same     | proceed                                      |
//	| present  | other    | refuse, naming both                          |
//
// The third row is what makes the guard hold. If an undeclared root were
// allowed through, deleting one line from a configuration would turn the
// protection off, and the failure it protects against — a second root deleting
// the first root's rows as declared nowhere — is silent and destructive.
//
// Adoption is opt-in as a consequence: a database is only ever claimed once
// some configuration declares a root, so an existing project that declares
// none carries on exactly as it did.
func CheckRoot(recorded, declared string, adopt bool) error {
	switch {
	case recorded == "":
		return nil
	case adopt:
		return nil
	case declared == recorded:
		return nil
	case declared == "":
		return fmt.Errorf("%w: it belongs to %q, and this configuration declares no root\n"+
			"  add `root: %s` to %s, or pass --root",
			ErrWrongRoot, recorded, recorded, ConfigFileName)
	}

	return fmt.Errorf("%w: it belongs to %q, and this configuration declares %q\n"+
		"  if you meant to move it, re-run with --adopt-root",
		ErrWrongRoot, recorded, declared)
}

// ConfigFileName is named here only so the refusal above can point at it. The
// config package owns the file; meta owns the marker.
const ConfigFileName = ".jokarc.yaml"

// ClaimRoot records the root that owns this database, if nothing has claimed
// it yet. Nothing happens when the root is empty or already recorded.
//
// DO NOTHING rather than DO UPDATE: a claim is made once, and a second root
// writing its own name over the first is precisely what CheckRoot exists to
// stop.
func ClaimRoot(ctx context.Context, db *sql.DB, root string) error {
	return writeRoot(ctx, db, root, `ON CONFLICT (key) DO NOTHING`)
}

// AdoptRoot moves the claim to this root, overwriting whatever held it. It is
// what --adopt-root does, and it is the only way a claim ever changes.
func AdoptRoot(ctx context.Context, db *sql.DB, root string) error {
	return writeRoot(ctx, db, root,
		`ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = CURRENT_TIMESTAMP`)
}

func writeRoot(ctx context.Context, db *sql.DB, root, onConflict string) error {
	if root == "" {
		return nil
	}

	if err := EnsureTable(ctx, db); err != nil {
		return err
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO `+Table+` (key, value, updated_at)
		VALUES ($1, $2, CURRENT_TIMESTAMP)
		`+onConflict, KeyStateRoot, root); err != nil {
		return fmt.Errorf("recording %s: %w", KeyStateRoot, err)
	}

	return nil
}
