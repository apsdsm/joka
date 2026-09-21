package entity

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/apsdsm/joka/internal/domains/entity/infra"
	"github.com/apsdsm/joka/internal/meta"
)

// materializeState writes the audit copy of the state to disk, after the
// transaction that wrote it has committed.
//
// It runs after the commit, not inside it, because a file cannot join a
// transaction. The database is the authoritative copy and is already
// consistent; this is the copy that can say the database is the wrong one or an
// older restore of the right one, which a copy inside it never can.
//
// A failure to write is reported and does not fail the command. The sync
// already happened, unwinding it is not possible, and an unwritten file is a
// finding `joka status` reports rather than a reason to claim the run failed.
func materializeState(ctx context.Context, db *sql.DB, profile, jokaVersion string) error {
	state, err := infra.NewPostgresStateBackend(db).Load(ctx)
	if err != nil {
		return fmt.Errorf("reading the state back to write the state file: %w", err)
	}

	markers, err := meta.Read(ctx, db)
	if err != nil {
		return fmt.Errorf("reading the state markers: %w", err)
	}

	doc := infra.StateDocument{
		Identity: markers.StateIdentity,
		Version:  markers.StateVersion,
		State:    state,
	}
	doc.Stamp(jokaVersion)

	return infra.WriteStateFile(infra.StateFilePath(profile), doc)
}
