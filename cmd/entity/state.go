package entity

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/apsdsm/joka/internal/domains/entity/app"
	"github.com/apsdsm/joka/internal/domains/entity/domain"
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
func materializeState(ctx context.Context, db *sql.DB, stateFile, profile, jokaVersion string) error {
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

	return infra.WriteStateFile(infra.StateFilePath(stateFile, profile), doc)
}

// reloadFiles re-reads the declared files joka just rewrote, so the hash and
// the values it is about to record are the ones now on disk.
func reloadFiles(declared []*domain.EntityFile, rewritten []string) error {
	changed := make(map[string]bool, len(rewritten))
	for _, path := range rewritten {
		changed[path] = true
	}

	for _, file := range declared {
		if !changed[file.Path] {
			continue
		}

		full := file.FullPath

		hash, err := app.HashFileContent(full)
		if err != nil {
			return err
		}

		reparsed, err := app.ParseEntityAction{Path: full}.Execute()
		if err != nil {
			return err
		}

		file.ContentHash = hash
		file.Entities = reparsed.Entities
	}

	return nil
}

// declaredIn returns the file from a loaded set, or an error naming it.
func declaredIn(files []*domain.EntityFile, path string) (*domain.EntityFile, error) {
	for _, file := range files {
		if file.Path == path {
			return file, nil
		}
	}
	return nil, fmt.Errorf("entity file not found in the entities directory: %s", path)
}

// fullPathsOf maps each loaded file's state key to where it is on disk, which
// is what the conflict write-back needs: a resolution names the file by its
// key, and with several entity roots the key cannot be joined onto one of them
// to find the file again.
func fullPathsOf(files []*domain.EntityFile) map[string]string {
	out := make(map[string]string, len(files))
	for _, f := range files {
		out[f.Path] = f.FullPath
	}

	return out
}
