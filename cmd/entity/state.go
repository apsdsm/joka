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

// LoadedSet is every seed file the roots contain, ready to plan against.
//
// It is DB-independent apart from the state it is compared to, which is what
// lets `joka apply` load the same set against a speculative transaction that
// `entity sync` loads against the live connection. One loader, so the two
// cannot disagree about what the files say.
type LoadedSet struct {
	// Files is every declared file, in discovery order. Set-level rules — an
	// _id claimed twice, an override naming nothing — are already checked.
	Files []*domain.EntityFile
	// Dirty names the files whose content hash moved since the last sync.
	Dirty map[string]bool
	// OverriddenBy is _id to the file whose overrides: block set columns on it.
	OverriddenBy map[string]string
}

// LoadSet discovers, hashes, parses and validates the seed files under the
// given roots, comparing content hashes against the state it is given.
func LoadSet(dirs []string, state *domain.State) (*LoadedSet, error) {
	found, err := infra.DiscoverEntityRoots(dirs)
	if err != nil {
		return nil, err
	}

	set := &LoadedSet{Dirty: map[string]bool{}}

	for _, disc := range found {
		hash, err := app.HashFileContent(disc.Full)
		if err != nil {
			return nil, err
		}

		// Every file is parsed, including ones the hash says are unchanged.
		// _id uniqueness is a property of the whole set, so an unchanged file
		// still has to be read to know what it claims. The hash decides
		// whether a file is written, not whether it is read.
		file, err := app.ParseEntityAction{Path: disc.Full}.Execute()
		if err != nil {
			return nil, err
		}
		file.Path = disc.Key
		file.FullPath = disc.Full
		file.ContentHash = hash
		set.Files = append(set.Files, file)

		stored, tracked := state.FileHash(disc.Key)
		switch app.FileStatusFor(tracked, stored, hash) {
		case domain.StatusNew, domain.StatusModified:
			set.Dirty[disc.Key] = true
		}
	}

	// Overrides merge before anything else looks at the set, so the plan, the
	// apply, the hashes and the write-back all read one merged declaration.
	overriddenBy, problems := app.ApplyOverrides(set.Files)
	problems = append(problems, app.ValidateEntitySet(set.Files)...)

	if err := app.EntitySetError(problems); err != nil {
		return nil, err
	}
	set.OverriddenBy = overriddenBy

	return set, nil
}
