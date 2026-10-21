package app

import (
	"context"
	"runtime"
	"strings"
	"sync"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// maxHashWorkers bounds how many argon2id hashes run at once. Each holds 64 MiB
// while it runs, so the bound is also a bound on the memory a sync takes.
const maxHashWorkers = 4

// hashJob is one argon2id column of one entity.
type hashJob struct {
	refID, column, plaintext string
}

// prehash computes, in parallel, every argon2id value the apply is about to
// write, and returns them by _id and column.
//
// A hash takes about fifty milliseconds of CPU by design, and the apply resolved
// them one at a time in the middle of a loop that was otherwise waiting on the
// database. A first sync of a set with a thousand passwords spent most of a
// minute there.
//
// It computes only what the apply will write, by asking the same question the
// apply does (columnsToResolve). A column it skips, or one whose argument cannot
// be resolved here, is left for the ordinary path, which reports the failure with
// the entity it concerns.
func (a ApplySetAction) prehash(ctx context.Context, ordered []*domain.EntityFile, state *domain.State) map[string]map[string]any {
	var jobs []hashJob

	for _, file := range ordered {
		for _, e := range flattenEntities(file.Entities, nil) {
			for _, column := range a.columnsToResolve(e, state) {
				expr, ok := templateExpr(e.Columns[column])
				if !ok || !strings.HasPrefix(expr, "argon2id|") {
					continue
				}

				plaintext, err := resolveHashArg(ctx, expr[len("argon2id|"):], a.Secrets)
				if err != nil {
					continue
				}

				jobs = append(jobs, hashJob{e.RefID, column, plaintext})
			}
		}
	}

	// One hash is no faster started elsewhere.
	if len(jobs) < 2 {
		return nil
	}

	workers := min(max(runtime.NumCPU()/2, 1), maxHashWorkers)

	type done struct {
		job  hashJob
		hash string
	}

	queue := make(chan hashJob)
	results := make(chan done, len(jobs))

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range queue {
				hash, err := hashArgon2id(job.plaintext)
				if err != nil {
					continue
				}
				results <- done{job, hash}
			}
		}()
	}

	for _, job := range jobs {
		queue <- job
	}
	close(queue)
	wg.Wait()
	close(results)

	out := make(map[string]map[string]any)
	for r := range results {
		if out[r.job.refID] == nil {
			out[r.job.refID] = make(map[string]any)
		}
		out[r.job.refID][r.job.column] = r.hash
	}

	return out
}

// columnsToResolve is the columns of an entity the apply will resolve: every
// one for a row it inserts, and for a row it updates only the ones it will
// write. The apply used to resolve all of them either way and then throw most
// away, which for an argon2id column meant a hash computed to be discarded.
func (a ApplySetAction) columnsToResolve(e domain.Entity, state *domain.State) []string {
	row, tracked := state.Row(e.RefID)
	_, adopted := a.Adopted[e.RefID]

	inserting := !tracked && !adopted
	if tracked && a.Recreate[e.RefID] {
		inserting = true
	}

	if inserting {
		names := make([]string, 0, len(e.Columns))
		for name := range e.Columns {
			names = append(names, name)
		}
		return names
	}

	if tracked && row.Table != e.Table {
		return nil
	}

	return writableNames(e, a.Keep[e.RefID], a.Write[e.RefID])
}

// writableNames narrows the planned columns to the ones this run writes: not
// _once, not conceded to the database, and actually declared.
func writableNames(e domain.Entity, kept map[string]string, planned []string) []string {
	var out []string

	for _, name := range planned {
		if e.IsOnce(name) {
			continue
		}
		if _, held := kept[name]; held {
			continue
		}
		if _, declared := e.Columns[name]; !declared {
			continue
		}
		out = append(out, name)
	}

	return out
}
