package status

import (
	"context"
	"path/filepath"
	"sort"

	entityapp "github.com/apsdsm/joka/internal/domains/entity/app"
	entitydomain "github.com/apsdsm/joka/internal/domains/entity/domain"
	entityinfra "github.com/apsdsm/joka/internal/domains/entity/infra"
)

// buildEntities reports every entity file across all three planes.
//
// `entity status` already covers declared-vs-tracked via the content hash. What
// it does not answer, and what this adds, is whether the rows joka tracks for a
// file are still in the database, and whether `entity sync` would actually be
// able to update the file or would refuse it as a structural change. Both are
// routinely the reason a sync is "behind" in a way the hash cannot show.
func buildEntities(ctx context.Context, in Inputs) (Entities, error) {
	out := Entities{Dir: in.EntitiesDir}

	// The state loads as empty whether the tracking is absent or merely empty,
	// so the missing-table finding still comes from a direct probe.
	//
	// It probes joka_state, the table tracking version 3 moved entity tracking
	// into. Probing joka_entities outlived the table: version 3 drops it, so
	// every up-to-date database reported "no entity file has been synced yet"
	// directly above the table listing the files it had synced.
	//
	// A version 1 or 2 database still has joka_entities and no joka_state, and
	// its state is read from the old tables, so both count as tracking being
	// present.
	hasTracking, err := in.Probe.TableExists(ctx, "joka_state")
	if err != nil {
		return out, err
	}
	if !hasTracking {
		legacy, err := in.Probe.TableExists(ctx, "joka_entities")
		if err != nil {
			return out, err
		}
		hasTracking = legacy
	}
	if !hasTracking {
		out.Skipped = "no entity tracking in this database — no entity file has been synced yet"
	}

	state := in.EntityState
	if state == nil {
		state = entitydomain.NewState()
	}

	paths, err := entityinfra.DiscoverEntityFiles(in.EntitiesDir)
	if err != nil {
		// A missing entities directory is a finding, not a failure: the
		// tracking table may still hold rows worth reporting as orphans.
		if out.Skipped == "" {
			out.Skipped = err.Error()
		}
	}

	// parsedFiles keeps every file that read cleanly, so the set-level _id check
	// can see across all of them.
	parsedFiles := make(map[string]*entitydomain.EntityFile)

	// tableExists caches existence checks, which repeat across files that seed
	// the same tables.
	tableExists := make(map[string]bool)

	seen := make(map[string]bool, len(paths))

	for _, rel := range paths {
		seen[rel] = true
		file := EntityFile{Path: rel, Declared: -1}

		full := filepath.Join(in.EntitiesDir, rel)

		hash, err := entityapp.HashFileContent(full)
		if err != nil {
			file.ParseError = err.Error()
		}

		stored, tracked := state.FileHash(rel)

		// The comparison is sync's own, so the report cannot disagree with what
		// a sync would consider modified.
		status := entityapp.FileStatusFor(tracked, stored, hash)
		file.Status = string(status)

		switch status {
		case entitydomain.StatusNew:
			out.Counts.New++
		case entitydomain.StatusModified:
			out.Counts.Modified++
		default:
			out.Counts.Synced++
		}

		parsed, err := entityapp.ParseEntityAction{Path: full}.Execute()
		if err != nil {
			if file.ParseError == "" {
				file.ParseError = err.Error()
			}
		} else {
			file.Declared = entityapp.CountEntities(parsed.Entities)
			parsed.Path = rel
			parsedFiles[rel] = parsed
		}

		trackedRows := state.RowsInFile(rel)
		file.Tracked = len(trackedRows)

		if err := countLiveRows(ctx, in, tableExists, trackedRows, &file); err != nil {
			return out, err
		}

		if parsed != nil {
			file.KeyedByID = allHaveRefID(parsed.Entities) && allRowsHaveRefID(trackedRows)

		}

		out.Files = append(out.Files, file)
	}

	// Tracked files with nothing on disk. `entity forget --orphans` is what
	// clears them.
	for path := range state.Files {
		if seen[path] {
			continue
		}

		file := EntityFile{
			Path:     path,
			Status:   string(entitydomain.StatusOrphaned),
			Declared: -1,
		}
		out.Counts.Orphaned++

		trackedRows := state.RowsInFile(path)
		file.Tracked = len(trackedRows)

		if err := countLiveRows(ctx, in, tableExists, trackedRows, &file); err != nil {
			return out, err
		}

		out.Files = append(out.Files, file)
	}

	// _id uniqueness is a property of the whole set, so it can only be checked
	// once every file has been read. Each problem is attached to every file it
	// names, so a duplicate shows against both claimants.
	files := make([]*entitydomain.EntityFile, 0, len(parsedFiles))
	for _, f := range parsedFiles {
		files = append(files, f)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	byPath := make(map[string][]entityapp.EntitySetProblem)
	for _, problem := range entityapp.ValidateEntitySet(files) {
		seen := make(map[string]bool)
		for _, loc := range problem.Where {
			if seen[loc.File] {
				continue
			}
			seen[loc.File] = true
			byPath[loc.File] = append(byPath[loc.File], problem)
		}
	}

	for i := range out.Files {
		out.Files[i].IdentityProblems = byPath[out.Files[i].Path]
	}

	sort.Slice(out.Files, func(i, j int) bool {
		return out.Files[i].Path < out.Files[j].Path
	})

	return out, nil
}

// countLiveRows fills in Live, MissingRows and MissingTables for a file by
// asking the database which of its tracked primary keys are still there. Rows
// are grouped by table and primary key column so each group costs one query
// rather than one per row.
func countLiveRows(ctx context.Context, in Inputs, tableExists map[string]bool, rows []entitydomain.TrackedRow, file *EntityFile) error {
	if len(rows) == 0 {
		return nil
	}

	type group struct {
		table    string
		pkColumn string
	}

	pks := make(map[group][]int64)
	for _, row := range rows {
		pkColumn := row.PKColumn
		if pkColumn == "" {
			pkColumn = "id"
		}
		g := group{table: row.TableName, pkColumn: pkColumn}
		pks[g] = append(pks[g], row.RowPK)
	}

	groups := make([]group, 0, len(pks))
	for g := range pks {
		groups = append(groups, g)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].table < groups[j].table })

	for _, g := range groups {
		exists, cached := tableExists[g.table]
		if !cached {
			var err error
			exists, err = in.Probe.TableExists(ctx, g.table)
			if err != nil {
				return err
			}
			tableExists[g.table] = exists
		}

		// A tracked row in a table a later migration dropped. Benign until
		// something tries to reimport the file, which would DELETE FROM a
		// table that is not there.
		if !exists {
			file.MissingRows += len(pks[g])
			file.MissingTables = append(file.MissingTables, g.table)
			continue
		}

		found, err := in.Probe.ExistingPKs(ctx, g.table, g.pkColumn, pks[g])
		if err != nil {
			return err
		}
		file.Live += len(found)
		file.MissingRows += len(pks[g]) - len(found)
	}

	sort.Strings(file.MissingTables)
	return nil
}

// allHaveRefID reports whether every entity in the graph declares an _id.
func allHaveRefID(entities []entitydomain.Entity) bool {
	for _, e := range entities {
		if e.RefID == "" || !allHaveRefID(e.Children) {
			return false
		}
	}
	return true
}

// allRowsHaveRefID reports whether every tracked row recorded a ref_id. Rows
// tracked before _id was recorded have an empty one.
func allRowsHaveRefID(rows []entitydomain.TrackedRow) bool {
	for _, r := range rows {
		if r.RefID == "" {
			return false
		}
	}
	return true
}
