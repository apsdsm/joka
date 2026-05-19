package app

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
)

// ModifiedTable describes a single table whose live schema differs from its
// snapshot.
type ModifiedTable struct {
	Table    string `json:"table"`
	Snapshot string `json:"snapshot"`
	Live     string `json:"live"`
}

// VerifyResult is the outcome of comparing the live schema against the latest
// snapshot.
type VerifyResult struct {
	MigrationIndex string          `json:"migration_index"`
	Added          []string        `json:"added"`    // in live but not in snapshot
	Removed        []string        `json:"removed"`  // in snapshot but not in live
	Modified       []ModifiedTable `json:"modified"` // in both, CREATE statements differ
}

// HasDrift reports whether any difference was found.
func (r VerifyResult) HasDrift() bool {
	return len(r.Added) > 0 || len(r.Removed) > 0 || len(r.Modified) > 0
}

// VerifySchemaAction compares the live database schema against the latest
// snapshot to surface drift introduced outside of joka migrations.
type VerifySchemaAction struct {
	DB DBAdapter
}

// Execute fetches the latest snapshot, computes the live schema, and returns
// the diff between them.
func (a VerifySchemaAction) Execute(ctx context.Context) (VerifyResult, error) {
	var result VerifyResult

	index, err := a.DB.GetLatestSnapshotIndex(ctx)
	if err != nil {
		return result, fmt.Errorf("getting latest snapshot index: %w", err)
	}
	result.MigrationIndex = index

	snapshotJSON, err := a.DB.GetSchemaSnapshot(ctx, index)
	if err != nil {
		return result, fmt.Errorf("loading snapshot: %w", err)
	}

	var snapshot map[string]string
	if err := json.Unmarshal([]byte(snapshotJSON), &snapshot); err != nil {
		return result, fmt.Errorf("parsing snapshot: %w", err)
	}

	live, err := a.DB.ComputeSchema(ctx)
	if err != nil {
		return result, fmt.Errorf("computing live schema: %w", err)
	}

	for table, liveStmt := range live {
		snapStmt, ok := snapshot[table]
		if !ok {
			result.Added = append(result.Added, table)
			continue
		}
		if normalizeCreateTable(snapStmt) != normalizeCreateTable(liveStmt) {
			result.Modified = append(result.Modified, ModifiedTable{
				Table:    table,
				Snapshot: snapStmt,
				Live:     liveStmt,
			})
		}
	}

	for table := range snapshot {
		if _, ok := live[table]; !ok {
			result.Removed = append(result.Removed, table)
		}
	}

	sort.Strings(result.Added)
	sort.Strings(result.Removed)
	sort.Slice(result.Modified, func(i, j int) bool {
		return result.Modified[i].Table < result.Modified[j].Table
	})

	return result, nil
}

// autoIncrementRE strips `AUTO_INCREMENT=<n>` from MySQL SHOW CREATE TABLE
// output so comparing snapshots against live schemas doesn't false-positive
// every time a row is inserted.
var autoIncrementRE = regexp.MustCompile(`\s*AUTO_INCREMENT=\d+`)

// normalizeCreateTable removes non-structural noise so two equivalent schemas
// compare equal. Currently strips MySQL's AUTO_INCREMENT counter.
func normalizeCreateTable(stmt string) string {
	return autoIncrementRE.ReplaceAllString(stmt, "")
}
