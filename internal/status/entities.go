package status

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	entityapp "github.com/apsdsm/joka/internal/domains/entity/app"
	entitydomain "github.com/apsdsm/joka/internal/domains/entity/domain"
	entityinfra "github.com/apsdsm/joka/internal/domains/entity/infra"
)

// EntityReader is the subset of the entity domain's DBAdapter that the report
// needs. It is satisfied by that adapter as-is.
type EntityReader interface {
	GetAllSyncedEntities(ctx context.Context) (map[string]string, error)
	GetTrackedRows(ctx context.Context, entityFile string) ([]entitydomain.TrackedRow, error)
}

// buildEntities reports every entity file across all three planes.
//
// `entity status` already covers declared-vs-tracked via the content hash. What
// it does not answer, and what this adds, is whether the rows joka tracks for a
// file are still in the database, and whether `entity sync` would actually be
// able to update the file or would refuse it as a structural change. Both are
// routinely the reason a sync is "behind" in a way the hash cannot show.
func buildEntities(ctx context.Context, in Inputs) (Entities, error) {
	out := Entities{Dir: in.EntitiesDir}

	hasTracking, err := in.Probe.TableExists(ctx, "joka_entities")
	if err != nil {
		return out, err
	}
	if !hasTracking {
		out.Skipped = "joka_entities does not exist — no entity file has been synced yet"
	}

	var synced map[string]string
	if hasTracking {
		synced, err = in.Entity.GetAllSyncedEntities(ctx)
		if err != nil {
			return out, fmt.Errorf("reading tracked entity files: %w", err)
		}
	}

	hasRowTracking, err := in.Probe.TableExists(ctx, "joka_entity_rows")
	if err != nil {
		return out, err
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

		dbHash, tracked := synced[rel]
		switch {
		case !tracked:
			file.Status = string(entitydomain.StatusNew)
			out.Counts.New++
		case dbHash == "" || dbHash != hash:
			// An empty stored hash predates content hashing; sync treats it as
			// modified, so status does too.
			file.Status = string(entitydomain.StatusModified)
			out.Counts.Modified++
		default:
			file.Status = string(entitydomain.StatusSynced)
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

		var trackedRows []entitydomain.TrackedRow
		if tracked && hasRowTracking {
			trackedRows, err = in.Entity.GetTrackedRows(ctx, rel)
			if err != nil {
				return out, fmt.Errorf("reading tracked rows for %s: %w", rel, err)
			}
		}
		file.Tracked = len(trackedRows)

		if err := countLiveRows(ctx, in, tableExists, trackedRows, &file); err != nil {
			return out, err
		}

		if parsed != nil {
			file.KeyedByID = allHaveRefID(parsed.Entities) && allRowsHaveRefID(trackedRows)

			// Only a modified file is ever put through the in-place update
			// path, so only a modified file can be refused for a structural
			// change. Reporting it for the others would be noise.
			if file.Status == string(entitydomain.StatusModified) && len(trackedRows) > 0 {
				if _, _, err := entityapp.AlignTrackedRows(rel, parsed.Entities, trackedRows); err != nil {
					file.Structural = err.Error()
				}
			}
		}

		out.Files = append(out.Files, file)
	}

	// Tracked files with nothing on disk. Nothing but `entity status` reports
	// these today and no command clears them.
	for path := range synced {
		if seen[path] {
			continue
		}

		file := EntityFile{
			Path:     path,
			Status:   string(entitydomain.StatusOrphaned),
			Declared: -1,
		}
		out.Counts.Orphaned++

		var trackedRows []entitydomain.TrackedRow
		if hasRowTracking {
			trackedRows, err = in.Entity.GetTrackedRows(ctx, path)
			if err != nil {
				return out, fmt.Errorf("reading tracked rows for %s: %w", path, err)
			}
		}
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
