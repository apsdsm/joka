package status_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	entitydomain "github.com/apsdsm/joka/internal/domains/entity/domain"
	entityinfra "github.com/apsdsm/joka/internal/domains/entity/infra"
	lockinfra "github.com/apsdsm/joka/internal/domains/lock/infra"
	migrationinfra "github.com/apsdsm/joka/internal/domains/migration/infra"
	templateinfra "github.com/apsdsm/joka/internal/domains/template/infra"
	"github.com/apsdsm/joka/internal/status"
	"github.com/apsdsm/joka/testlib"
)

// buildAgainst runs the report with the real adapters.
func buildAgainst(t *testing.T, db *sql.DB, in status.Inputs) status.Report {
	t.Helper()

	ctx := context.Background()

	in.Driver = "postgres"
	in.Migration = migrationinfra.NewPostgresDBAdapter(db)
	in.Lock = lockinfra.NewPostgresLockAdapter(db)
	in.Probe = status.NewProbe(db)

	// Loading the state is part of what has to stay read-only, so it runs here
	// rather than being faked — TestStatusIsReadOnly covers this path too.
	entityState, err := entityinfra.NewPostgresStateBackend(db).Load(ctx)
	if err != nil {
		t.Fatalf("loading entity state: %v", err)
	}
	in.EntityState = entityState

	report, err := status.Build(ctx, in)
	if err != nil {
		t.Fatalf("building the report: %v", err)
	}
	return report
}

// jokaTables lists the joka_* tables present in the test database.
func jokaTables(t *testing.T, db *sql.DB) []string {
	t.Helper()

	rows, err := db.Query(`
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name LIKE 'joka_%'
		ORDER BY table_name
	`)
	if err != nil {
		t.Fatalf("listing joka tables: %v", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scanning table name: %v", err)
		}
		names = append(names, name)
	}
	return names
}

// TestStatusIsReadOnly is the guarantee that separates status from every other
// command: the others auto-create their tracking tables on the way past, which
// would make a missing table unreportable. Status must leave a database with no
// joka_* tables exactly as it found it.
func TestStatusIsReadOnly(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	for _, table := range jokaTables(t, db) {
		testlib.DropTablePostgres(t, db, table)
	}

	before := jokaTables(t, db)
	if len(before) != 0 {
		t.Fatalf("expected no joka tables before the report, got %v", before)
	}

	dir := t.TempDir()
	report := buildAgainst(t, db, status.Inputs{
		MigrationsDir: dir,
		EntitiesDir:   dir,
		TemplatesDir:  dir,
	})

	if after := jokaTables(t, db); len(after) != 0 {
		t.Errorf("status created tables: %v", after)
	}

	if report.Migrations.Skipped == "" {
		t.Error("expected the migrations section to say why it is empty")
	}
	if report.Entities.Skipped == "" {
		t.Error("expected the entities section to say why it is empty")
	}

	action, ok := firstWithCommand(report.Actions, "joka init")
	if !ok {
		t.Fatalf("expected an init action on a bare database, got %+v", report.Actions)
	}
	if action.Scope != status.ScopeMigrations {
		t.Errorf("expected the init action scoped to migrations, got %q", action.Scope)
	}
}

// TestStatusAgainstLiveTracking exercises the paths that need real tracking
// tables: tracked rows counted against the database, and a tracked row whose
// table has been dropped.
func TestStatusAgainstLiveTracking(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	db, err := testlib.GetTestPostgresDB()
	if err != nil {
		t.Fatalf("getting test db: %v", err)
	}

	ctx := context.Background()

	for _, table := range jokaTables(t, db) {
		testlib.DropTablePostgres(t, db, table)
	}
	t.Cleanup(func() {
		for _, table := range jokaTables(t, db) {
			testlib.DropTablePostgres(t, db, table)
		}
		testlib.DropTablePostgres(t, db, "widgets")
	})

	if _, err := db.ExecContext(ctx, `CREATE TABLE widgets (id serial PRIMARY KEY, name text)`); err != nil {
		t.Fatalf("creating widgets: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO widgets (id, name) VALUES (1, 'kept')`); err != nil {
		t.Fatalf("seeding widgets: %v", err)
	}

	backend := entityinfra.NewPostgresStateBackend(db)
	if err := backend.EnsureStateTable(ctx); err != nil {
		t.Fatalf("ensuring joka_state: %v", err)
	}

	// widgets.yaml is on disk and tracks two rows, one of which was deleted
	// out of band. gone.yaml is tracked but its file and its table are both
	// gone.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "widgets.yaml"), []byte(
		"entities:\n  - _is: widgets\n    _id: kept\n    name: kept\n  - _is: widgets\n    _id: lost\n    name: lost\n",
	), 0o644); err != nil {
		t.Fatalf("writing widgets.yaml: %v", err)
	}

	state := entitydomain.NewState()
	state.TrackFile("widgets.yaml", "stale-hash")
	state.TrackFile("gone.yaml", "whatever")
	state.Track("kept", entitydomain.EntityState{
		Table: "widgets", PKColumn: "id", PKValue: 1, File: "widgets.yaml", Order: 0,
	})
	state.Track("lost", entitydomain.EntityState{
		Table: "widgets", PKColumn: "id", PKValue: 999, File: "widgets.yaml", Order: 1,
	})
	state.Track("orphan_row", entitydomain.EntityState{
		Table: "dropped_table", PKColumn: "id", PKValue: 5, File: "gone.yaml", Order: 0,
	})

	if err := backend.Save(ctx, state); err != nil {
		t.Fatalf("saving the entity state: %v", err)
	}

	report := buildAgainst(t, db, status.Inputs{
		MigrationsDir: t.TempDir(),
		EntitiesDir:   dir,
		TemplatesDir:  dir,
		Tables:        []templateinfra.TableConfig{{Name: "widgets", Strategy: "truncate"}},
	})

	widgets := entityFile(t, report, "widgets.yaml")
	if widgets.Declared != 2 || widgets.Tracked != 2 {
		t.Errorf("expected 2 declared and 2 tracked, got %d and %d", widgets.Declared, widgets.Tracked)
	}
	if widgets.Live != 1 || widgets.MissingRows != 1 {
		t.Errorf("expected 1 live and 1 missing row, got %d and %d", widgets.Live, widgets.MissingRows)
	}

	gone := entityFile(t, report, "gone.yaml")
	if gone.Status != "orphaned" {
		t.Errorf("expected gone.yaml orphaned, got %q", gone.Status)
	}
	if len(gone.MissingTables) != 1 || gone.MissingTables[0] != "dropped_table" {
		t.Errorf("expected the dropped table named, got %v", gone.MissingTables)
	}

	// The widgets directory has no record files, so the template table declares
	// nothing while the table itself holds a row.
	if len(report.Templates.Tables) != 1 {
		t.Fatalf("expected one template table, got %d", len(report.Templates.Tables))
	}
	if got := report.Templates.Tables[0].Live; got != 1 {
		t.Errorf("expected 1 live template row, got %d", got)
	}
}

func entityFile(t *testing.T, r status.Report, path string) status.EntityFile {
	t.Helper()
	for _, f := range r.Entities.Files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("entity file %s not in the report", path)
	return status.EntityFile{}
}

func firstWithCommand(actions []status.Action, command string) (status.Action, bool) {
	for _, a := range actions {
		if a.Command == command {
			return a, true
		}
	}
	return status.Action{}, false
}
