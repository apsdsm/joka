package status

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/apsdsm/joka/internal/domains/migration/infra"
	"github.com/apsdsm/joka/internal/domains/migration/infra/models"
)

// MigrationReader is the subset of the migration domain's DBAdapter that the
// report needs. It is satisfied by that adapter as-is.
type MigrationReader interface {
	HasMigrationsTable(ctx context.Context) (bool, error)
	GetAppliedMigrations(ctx context.Context) ([]models.MigrationRow, error)
	GetLatestSnapshotIndex(ctx context.Context) (string, error)
	GetSchemaSnapshot(ctx context.Context, migrationIndex string) (string, error)
	ComputeSchema(ctx context.Context) (map[string]string, error)
}

// buildMigrations aligns the migration files on disk against the rows in
// joka_migrations by index.
//
// Alignment is by index, not by position. GetMigrationChainAction (which backs
// `migrate status` and `migrate up`) walks both lists positionally and returns
// an error the moment they disagree, so on exactly the databases worth
// diagnosing it reports nothing at all. Indexes are timestamps, so sorting
// them lexicographically orders them chronologically, and every index present
// on either side gets a row.
func buildMigrations(ctx context.Context, in Inputs) (Migrations, error) {
	out := Migrations{Dir: in.MigrationsDir}

	hasTable, err := in.Migration.HasMigrationsTable(ctx)
	if err != nil {
		return out, fmt.Errorf("checking for the migrations table: %w", err)
	}
	if !hasTable {
		out.Skipped = "joka_migrations does not exist — run joka init"
		out.Drift.Skipped = "no migrations table"
		return out, nil
	}

	files, err := infra.ListMigrationFiles(in.MigrationsDir)
	if err != nil {
		out.Skipped = err.Error()
	}

	applied, err := in.Migration.GetAppliedMigrations(ctx)
	if err != nil {
		return out, fmt.Errorf("reading applied migrations: %w", err)
	}

	byIndex := make(map[string]*Migration)
	order := make([]string, 0, len(files)+len(applied))

	at := func(index string) *Migration {
		m, ok := byIndex[index]
		if !ok {
			m = &Migration{Index: index}
			byIndex[index] = m
			order = append(order, index)
		}
		return m
	}

	for _, f := range files {
		m := at(f.Index)
		m.Declared = true
		m.Name = f.Name
	}

	maxApplied := ""
	for _, row := range applied {
		m := at(row.MigrationIndex)
		m.Tracked = true
		m.AppliedAt = row.AppliedAt.Format("2006-01-02 15:04:05")
		if row.MigrationIndex > maxApplied {
			maxApplied = row.MigrationIndex
		}
	}

	sort.Strings(order)

	for _, index := range order {
		m := byIndex[index]
		switch {
		case m.Tracked && m.Declared:
			m.Status = MigrationApplied
			out.Applied++
		case m.Tracked:
			m.Status = MigrationFileMissing
			out.FileMissing++
		// A file that is not applied but sorts before something that is would
		// have to be applied out of order to catch up, which `migrate up`
		// cannot do.
		case index < maxApplied:
			m.Status = MigrationOutOfOrder
			out.OutOfOrder++
		default:
			m.Status = MigrationPending
			out.Pending++
		}
		out.Files = append(out.Files, *m)
	}

	out.Drift = buildDrift(ctx, in)

	return out, nil
}

// buildDrift compares the live schema against the snapshot stored for the most
// recent migration. It repeats VerifySchemaAction's comparison rather than
// calling it because the action's adapter methods create joka_snapshots on the
// way past, and status must not create anything: the check is gated on the
// table already being there.
func buildDrift(ctx context.Context, in Inputs) Drift {
	var out Drift

	hasSnapshots, err := in.Probe.TableExists(ctx, "joka_snapshots")
	if err != nil {
		out.Skipped = err.Error()
		return out
	}
	if !hasSnapshots {
		out.Skipped = "joka_snapshots does not exist — no snapshot has been captured yet"
		return out
	}

	index, err := in.Migration.GetLatestSnapshotIndex(ctx)
	if err != nil {
		out.Skipped = err.Error()
		return out
	}
	out.SnapshotIndex = index

	raw, err := in.Migration.GetSchemaSnapshot(ctx, index)
	if err != nil {
		out.Skipped = fmt.Sprintf("loading snapshot %s: %v", index, err)
		return out
	}

	var snapshot map[string]string
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		out.Skipped = fmt.Sprintf("parsing snapshot %s: %v", index, err)
		return out
	}

	live, err := in.Migration.ComputeSchema(ctx)
	if err != nil {
		out.Skipped = fmt.Sprintf("computing the live schema: %v", err)
		return out
	}

	out.Checked = true

	for table, liveStmt := range live {
		snapStmt, ok := snapshot[table]
		if !ok {
			out.Added = append(out.Added, table)
			continue
		}
		if snapStmt != liveStmt {
			onlyLive, onlySnap := diffLines(snapStmt, liveStmt)
			out.Modified = append(out.Modified, ModifiedTable{
				Table:          table,
				Snapshot:       snapStmt,
				Live:           liveStmt,
				OnlyInLive:     onlyLive,
				OnlyInSnapshot: onlySnap,
			})
		}
	}

	for table := range snapshot {
		if _, ok := live[table]; !ok {
			out.Removed = append(out.Removed, table)
		}
	}

	sort.Strings(out.Added)
	sort.Strings(out.Removed)
	sort.Slice(out.Modified, func(i, j int) bool {
		return out.Modified[i].Table < out.Modified[j].Table
	})

	return out
}

// diffLines returns the lines present in only one of two CREATE statements,
// trimmed of whitespace and trailing commas. It is a set difference rather
// than a real diff: enough to name which columns or constraints differ without
// reprinting both statements, which is what `migrate verify` is for.
func diffLines(snapshot, live string) (onlyInLive, onlyInSnapshot []string) {
	snapSet := lineSet(snapshot)
	liveSet := lineSet(live)

	for line := range liveSet {
		if _, ok := snapSet[line]; !ok {
			onlyInLive = append(onlyInLive, line)
		}
	}
	for line := range snapSet {
		if _, ok := liveSet[line]; !ok {
			onlyInSnapshot = append(onlyInSnapshot, line)
		}
	}

	sort.Strings(onlyInLive)
	sort.Strings(onlyInSnapshot)
	return onlyInLive, onlyInSnapshot
}

func lineSet(stmt string) map[string]struct{} {
	set := make(map[string]struct{})
	for _, line := range strings.Split(stmt, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), ","))
		if line == "" {
			continue
		}
		set[line] = struct{}{}
	}
	return set
}
