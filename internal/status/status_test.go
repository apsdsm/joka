package status

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	entityapp "github.com/apsdsm/joka/internal/domains/entity/app"
	entitydomain "github.com/apsdsm/joka/internal/domains/entity/domain"
	lockdomain "github.com/apsdsm/joka/internal/domains/lock/domain"
	"github.com/apsdsm/joka/internal/domains/migration/infra/models"
	templatedomain "github.com/apsdsm/joka/internal/domains/template/domain"
	templateinfra "github.com/apsdsm/joka/internal/domains/template/infra"
	"github.com/apsdsm/joka/internal/textui"
)

// --- fakes -----------------------------------------------------------------

type fakeProbe struct {
	tables map[string]bool
	counts map[string]int
	// missingPKs are primary keys the probe reports as gone, keyed by table.
	missingPKs map[string]map[int64]bool
}

func (f fakeProbe) TableExists(_ context.Context, table string) (bool, error) {
	return f.tables[table], nil
}

func (f fakeProbe) CountRows(_ context.Context, table string) (int, error) {
	return f.counts[table], nil
}

func (f fakeProbe) ExistingPKs(_ context.Context, table, _ string, pks []int64) (map[int64]struct{}, error) {
	found := make(map[int64]struct{})
	for _, pk := range pks {
		if f.missingPKs[table][pk] {
			continue
		}
		found[pk] = struct{}{}
	}
	return found, nil
}

type fakeMigrations struct {
	hasTable bool
	applied  []models.MigrationRow
	snapshot map[string]string
	live     map[string]string
	index    string
	indexErr error
}

func (f fakeMigrations) HasMigrationsTable(context.Context) (bool, error) {
	return f.hasTable, nil
}

func (f fakeMigrations) GetAppliedMigrations(context.Context) ([]models.MigrationRow, error) {
	return f.applied, nil
}

func (f fakeMigrations) GetLatestSnapshotIndex(context.Context) (string, error) {
	return f.index, f.indexErr
}

func (f fakeMigrations) GetSchemaSnapshot(context.Context, string) (string, error) {
	raw, err := json.Marshal(f.snapshot)
	return string(raw), err
}

func (f fakeMigrations) ComputeSchema(context.Context) (map[string]string, error) {
	return f.live, nil
}

type fakeEntities struct {
	synced map[string]string
	rows   map[string][]entitydomain.TrackedRow
}

func (f fakeEntities) GetAllSyncedEntities(context.Context) (map[string]string, error) {
	return f.synced, nil
}

func (f fakeEntities) GetTrackedRows(_ context.Context, file string) ([]entitydomain.TrackedRow, error) {
	return f.rows[file], nil
}

type fakeLock struct{ held *lockdomain.Lock }

func (f fakeLock) GetLock(context.Context) (*lockdomain.Lock, error) { return f.held, nil }

// --- helpers ---------------------------------------------------------------

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	full := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("creating directory for %s: %v", name, err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}

func applied(index string) models.MigrationRow {
	return models.MigrationRow{MigrationIndex: index, AppliedAt: time.Unix(0, 0).UTC()}
}

func findMigration(t *testing.T, m Migrations, index string) Migration {
	t.Helper()
	for _, f := range m.Files {
		if f.Index == index {
			return f
		}
	}
	t.Fatalf("migration %s not in the report", index)
	return Migration{}
}

func findEntity(t *testing.T, e Entities, path string) EntityFile {
	t.Helper()
	for _, f := range e.Files {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("entity file %s not in the report", path)
	return EntityFile{}
}

func findAction(actions []Action, subject string) (Action, bool) {
	for _, a := range actions {
		if a.Subject == subject {
			return a, true
		}
	}
	return Action{}, false
}

// --- migrations ------------------------------------------------------------

func TestBuildMigrations(t *testing.T) {
	t.Run("it reports applied and pending migrations", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "240101000000_first.sql", "SELECT 1;")
		writeFile(t, dir, "240102000000_second.sql", "SELECT 1;")

		in := Inputs{
			MigrationsDir: dir,
			Migration:     fakeMigrations{hasTable: true, applied: []models.MigrationRow{applied("240101000000")}},
			Probe:         fakeProbe{},
		}

		m, err := buildMigrations(context.Background(), in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if m.Applied != 1 || m.Pending != 1 {
			t.Errorf("expected 1 applied and 1 pending, got %d and %d", m.Applied, m.Pending)
		}
		if got := findMigration(t, m, "240102000000"); got.Status != MigrationPending {
			t.Errorf("expected pending, got %q", got.Status)
		}
		if got := findMigration(t, m, "240101000000"); !got.Declared || !got.Tracked {
			t.Errorf("expected the applied migration declared and tracked, got %+v", got)
		}
	})

	t.Run("it reports a tracked migration with no file instead of failing", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "240102000000_second.sql", "SELECT 1;")

		in := Inputs{
			MigrationsDir: dir,
			Migration: fakeMigrations{hasTable: true, applied: []models.MigrationRow{
				applied("240101000000"),
				applied("240102000000"),
			}},
			Probe: fakeProbe{},
		}

		m, err := buildMigrations(context.Background(), in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		got := findMigration(t, m, "240101000000")
		if got.Status != MigrationFileMissing {
			t.Errorf("expected file_missing, got %q", got.Status)
		}
		if got.Declared {
			t.Error("expected the missing file not to be declared")
		}
		if m.FileMissing != 1 {
			t.Errorf("expected 1 file missing, got %d", m.FileMissing)
		}
	})

	t.Run("it reports a file that sorts before an applied migration as out of order", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "240101000000_first.sql", "SELECT 1;")
		writeFile(t, dir, "240102000000_second.sql", "SELECT 1;")

		in := Inputs{
			MigrationsDir: dir,
			Migration:     fakeMigrations{hasTable: true, applied: []models.MigrationRow{applied("240102000000")}},
			Probe:         fakeProbe{},
		}

		m, err := buildMigrations(context.Background(), in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if got := findMigration(t, m, "240101000000"); got.Status != MigrationOutOfOrder {
			t.Errorf("expected out_of_order, got %q", got.Status)
		}
		if m.OutOfOrder != 1 {
			t.Errorf("expected 1 out of order, got %d", m.OutOfOrder)
		}
	})

	t.Run("it skips the section when there is no migrations table", func(t *testing.T) {
		in := Inputs{
			MigrationsDir: t.TempDir(),
			Migration:     fakeMigrations{hasTable: false},
			Probe:         fakeProbe{},
		}

		m, err := buildMigrations(context.Background(), in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.Skipped == "" {
			t.Error("expected the section to record why it is empty")
		}
		if len(m.Files) != 0 {
			t.Errorf("expected no files, got %d", len(m.Files))
		}
	})
}

func TestBuildDrift(t *testing.T) {
	t.Run("it names the lines that differ", func(t *testing.T) {
		in := Inputs{
			Migration: fakeMigrations{
				hasTable: true,
				index:    "240101000000",
				snapshot: map[string]string{"users": "CREATE TABLE users (\n  id int,\n  name text\n)"},
				live:     map[string]string{"users": "CREATE TABLE users (\n  id int,\n  name text,\n  email text\n)"},
			},
			Probe: fakeProbe{tables: map[string]bool{"joka_snapshots": true}},
		}

		d := buildDrift(context.Background(), in)

		if !d.HasDrift() || len(d.Modified) != 1 {
			t.Fatalf("expected one modified table, got %+v", d)
		}
		if got := d.Modified[0].OnlyInLive; len(got) != 1 || got[0] != "email text" {
			t.Errorf("expected the added column named, got %v", got)
		}
		if got := d.Modified[0].OnlyInSnapshot; len(got) != 0 {
			t.Errorf("expected nothing only in the snapshot, got %v", got)
		}
	})

	t.Run("it reports no drift when the statements match", func(t *testing.T) {
		const create = "CREATE TABLE users (id integer NOT NULL);"
		in := Inputs{
			Migration: fakeMigrations{
				hasTable: true,
				index:    "240101000000",
				snapshot: map[string]string{"users": create},
				live:     map[string]string{"users": create},
			},
			Probe: fakeProbe{tables: map[string]bool{"joka_snapshots": true}},
		}

		if d := buildDrift(context.Background(), in); d.HasDrift() {
			t.Errorf("expected no drift, got %+v", d)
		}
	})

	t.Run("it skips the check when no snapshots table exists", func(t *testing.T) {
		in := Inputs{
			Migration: fakeMigrations{hasTable: true},
			Probe:     fakeProbe{},
		}

		d := buildDrift(context.Background(), in)
		if d.Checked {
			t.Error("expected the check to be skipped")
		}
		if d.Skipped == "" {
			t.Error("expected a reason for skipping")
		}
	})
}

// --- entities --------------------------------------------------------------

const twoEntityFile = `entities:
  - _is: users
    _id: admin
    name: Admin
    _has:
      - _is: profiles
        _id: admin_profile
        bio: hi
`

const threeEntityFile = twoEntityFile + `  - _is: users
    _id: second
    name: Second
`

func entityInputs(dir string, e fakeEntities, p fakeProbe) Inputs {
	return Inputs{EntitiesDir: dir, Entity: e, Probe: p}
}

func TestBuildEntities(t *testing.T) {
	t.Run("it counts declared, tracked and live rows", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "a.yaml", twoEntityFile)

		hash := hashOf(t, dir, "a.yaml")

		in := entityInputs(dir,
			fakeEntities{
				synced: map[string]string{"a.yaml": hash},
				rows: map[string][]entitydomain.TrackedRow{"a.yaml": {
					{TableName: "users", RowPK: 1, PKColumn: "id", RefID: "admin", InsertionOrder: 0},
					{TableName: "profiles", RowPK: 2, PKColumn: "id", RefID: "admin_profile", InsertionOrder: 1},
				}},
			},
			fakeProbe{tables: map[string]bool{"joka_entities": true, "joka_entity_rows": true, "users": true, "profiles": true}},
		)

		e, err := buildEntities(context.Background(), in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		file := findEntity(t, e, "a.yaml")
		if file.Status != string(entitydomain.StatusSynced) {
			t.Errorf("expected synced, got %q", file.Status)
		}
		if file.Declared != 2 || file.Tracked != 2 || file.Live != 2 {
			t.Errorf("expected 2/2/2 declared/tracked/live, got %d/%d/%d", file.Declared, file.Tracked, file.Live)
		}
		if !file.KeyedByID {
			t.Error("expected keyed_by_id when every entity and row has an _id")
		}
	})

	t.Run("it reports tracked rows that are no longer in the database", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "a.yaml", twoEntityFile)

		in := entityInputs(dir,
			fakeEntities{
				synced: map[string]string{"a.yaml": hashOf(t, dir, "a.yaml")},
				rows: map[string][]entitydomain.TrackedRow{"a.yaml": {
					{TableName: "users", RowPK: 1, PKColumn: "id", InsertionOrder: 0},
					{TableName: "profiles", RowPK: 2, PKColumn: "id", InsertionOrder: 1},
				}},
			},
			fakeProbe{
				tables:     map[string]bool{"joka_entities": true, "joka_entity_rows": true, "users": true, "profiles": true},
				missingPKs: map[string]map[int64]bool{"profiles": {2: true}},
			},
		)

		e, err := buildEntities(context.Background(), in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		file := findEntity(t, e, "a.yaml")
		if file.Live != 1 || file.MissingRows != 1 {
			t.Errorf("expected 1 live and 1 missing, got %d and %d", file.Live, file.MissingRows)
		}
	})

	t.Run("it names a tracked table that no longer exists", func(t *testing.T) {
		dir := t.TempDir()

		in := entityInputs(dir,
			fakeEntities{
				synced: map[string]string{"gone.yaml": "abc"},
				rows: map[string][]entitydomain.TrackedRow{"gone.yaml": {
					{TableName: "slot_assignments", RowPK: 7, PKColumn: "id", InsertionOrder: 0},
				}},
			},
			fakeProbe{tables: map[string]bool{"joka_entities": true, "joka_entity_rows": true}},
		)

		e, err := buildEntities(context.Background(), in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		file := findEntity(t, e, "gone.yaml")
		if file.Status != string(entitydomain.StatusOrphaned) {
			t.Errorf("expected orphaned, got %q", file.Status)
		}
		if len(file.MissingTables) != 1 || file.MissingTables[0] != "slot_assignments" {
			t.Errorf("expected the dropped table named, got %v", file.MissingTables)
		}
		if file.MissingRows != 1 {
			t.Errorf("expected the row counted as missing, got %d", file.MissingRows)
		}
	})

	t.Run("it predicts the structural refusal sync would give", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "a.yaml", threeEntityFile)

		in := entityInputs(dir,
			fakeEntities{
				// A stale hash makes the file modified, which is the only
				// state sync puts through the in-place update path.
				synced: map[string]string{"a.yaml": "stale"},
				rows: map[string][]entitydomain.TrackedRow{"a.yaml": {
					{TableName: "users", RowPK: 1, PKColumn: "id", RefID: "admin", InsertionOrder: 0},
					{TableName: "profiles", RowPK: 2, PKColumn: "id", RefID: "admin_profile", InsertionOrder: 1},
				}},
			},
			fakeProbe{tables: map[string]bool{"joka_entities": true, "joka_entity_rows": true, "users": true, "profiles": true}},
		)

		e, err := buildEntities(context.Background(), in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		file := findEntity(t, e, "a.yaml")
		if file.Status != string(entitydomain.StatusModified) {
			t.Errorf("expected modified, got %q", file.Status)
		}
		if file.Structural == "" {
			t.Fatal("expected the structural refusal to be predicted")
		}
		if !strings.Contains(file.Structural, "3 entities but 2 are tracked") {
			t.Errorf("expected sync's own message, got %q", file.Structural)
		}
	})

	t.Run("it does not report a structural change for an unmodified file", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "a.yaml", twoEntityFile)

		in := entityInputs(dir,
			fakeEntities{
				synced: map[string]string{"a.yaml": hashOf(t, dir, "a.yaml")},
				rows: map[string][]entitydomain.TrackedRow{"a.yaml": {
					{TableName: "users", RowPK: 1, PKColumn: "id", InsertionOrder: 0},
				}},
			},
			fakeProbe{tables: map[string]bool{"joka_entities": true, "joka_entity_rows": true, "users": true}},
		)

		e, err := buildEntities(context.Background(), in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if file := findEntity(t, e, "a.yaml"); file.Structural != "" {
			t.Errorf("expected no structural finding on a synced file, got %q", file.Structural)
		}
	})

	t.Run("it reports a new file with nothing tracked", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "a.yaml", twoEntityFile)

		in := entityInputs(dir, fakeEntities{}, fakeProbe{tables: map[string]bool{"joka_entities": true}})

		e, err := buildEntities(context.Background(), in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		file := findEntity(t, e, "a.yaml")
		if file.Status != string(entitydomain.StatusNew) {
			t.Errorf("expected new, got %q", file.Status)
		}
		if file.Declared != 2 || file.Tracked != 0 {
			t.Errorf("expected 2 declared and 0 tracked, got %d and %d", file.Declared, file.Tracked)
		}
		if e.Counts.New != 1 {
			t.Errorf("expected 1 new, got %d", e.Counts.New)
		}
	})

	t.Run("it records a parse failure against the file", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "bad.yaml", "entities:\n  - name: no table key\n")

		in := entityInputs(dir, fakeEntities{}, fakeProbe{tables: map[string]bool{"joka_entities": true}})

		e, err := buildEntities(context.Background(), in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		file := findEntity(t, e, "bad.yaml")
		if file.ParseError == "" {
			t.Error("expected the parse error recorded")
		}
		if file.Declared != -1 {
			t.Errorf("expected declared unknown (-1), got %d", file.Declared)
		}
	})
}

// --- templates -------------------------------------------------------------

func TestBuildTemplates(t *testing.T) {
	t.Run("it compares declared rows against the table", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "settings/defaults.csv", "key,value\na,1\nb,2\n")
		writeFile(t, dir, "settings/extra.yaml", "key: c\nvalue: 3\n")

		in := Inputs{
			TemplatesDir: dir,
			Tables:       []templateinfra.TableConfig{{Name: "settings", Strategy: templatedomain.StrategyTruncate}},
			Probe:        fakeProbe{tables: map[string]bool{"settings": true}, counts: map[string]int{"settings": 9}},
		}

		tpl, err := buildTemplates(context.Background(), in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if len(tpl.Tables) != 1 {
			t.Fatalf("expected one table, got %d", len(tpl.Tables))
		}
		table := tpl.Tables[0]
		if table.Files != 2 || table.Declared != 3 || table.Live != 9 {
			t.Errorf("expected 2 files, 3 declared, 9 live; got %d, %d, %d", table.Files, table.Declared, table.Live)
		}
	})

	t.Run("it reports a table that does not exist", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "settings/defaults.csv", "key,value\na,1\n")

		in := Inputs{
			TemplatesDir: dir,
			Tables:       []templateinfra.TableConfig{{Name: "settings", Strategy: templatedomain.StrategyTruncate}},
			Probe:        fakeProbe{},
		}

		tpl, err := buildTemplates(context.Background(), in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if !tpl.Tables[0].TableMissing || tpl.Tables[0].Live != -1 {
			t.Errorf("expected the table reported missing, got %+v", tpl.Tables[0])
		}
	})

	t.Run("it reports one bad directory against its own table", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "settings/defaults.csv", "key,value\na,1\n")

		in := Inputs{
			TemplatesDir: dir,
			Tables: []templateinfra.TableConfig{
				{Name: "settings", Strategy: templatedomain.StrategyTruncate},
				{Name: "missing_dir", Strategy: templatedomain.StrategyTruncate},
			},
			Probe: fakeProbe{tables: map[string]bool{"settings": true}, counts: map[string]int{"settings": 1}},
		}

		tpl, err := buildTemplates(context.Background(), in)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		if tpl.Skipped != "" {
			t.Errorf("expected the section to survive one bad table, got skipped %q", tpl.Skipped)
		}
		if tpl.Tables[0].LoadError != "" {
			t.Errorf("expected the good table unaffected, got %q", tpl.Tables[0].LoadError)
		}
		if tpl.Tables[1].LoadError == "" {
			t.Error("expected the missing directory recorded against its table")
		}
	})
}

// --- actions ---------------------------------------------------------------

func TestDeriveActions(t *testing.T) {
	t.Run("it names migrate up for pending migrations", func(t *testing.T) {
		r := Report{Migrations: Migrations{Pending: 2}}
		actions := deriveActions(r)
		if len(actions) != 1 || actions[0].Command != "joka migrate up" {
			t.Fatalf("expected migrate up, got %+v", actions)
		}
		if !strings.Contains(actions[0].Reason, "2 pending migrations") {
			t.Errorf("expected the count in the reason, got %q", actions[0].Reason)
		}
	})

	t.Run("it names reimport for a structural change", func(t *testing.T) {
		r := Report{Entities: Entities{Files: []EntityFile{
			{Path: "a.yaml", Status: "modified", Structural: "changed structurally"},
		}}}

		action, ok := findAction(deriveActions(r), "a.yaml")
		if !ok {
			t.Fatal("expected an action for the file")
		}
		if action.Command != "joka entity reimport a.yaml" {
			t.Errorf("expected reimport, got %q", action.Command)
		}
	})

	t.Run("it names entity forget for an orphan", func(t *testing.T) {
		r := Report{Entities: Entities{Files: []EntityFile{
			{Path: "gone.yaml", Status: "orphaned", Tracked: 3, Declared: -1},
		}}}

		action, ok := findAction(deriveActions(r), "gone.yaml")
		if !ok {
			t.Fatal("expected an action for the orphan")
		}
		if action.Command != "joka entity forget gone.yaml" {
			t.Errorf("expected entity forget, got %q", action.Command)
		}
		if !strings.Contains(action.Reason, "3 rows") {
			t.Errorf("expected the tracked row count in the reason, got %q", action.Reason)
		}
	})

	t.Run("it leaves a finding joka cannot fix without a command", func(t *testing.T) {
		// A template table that does not exist needs a migration, which is not
		// something joka can write.
		r := Report{Templates: Templates{Tables: []TemplateTable{
			{Name: "settings", Strategy: "truncate", TableMissing: true, Live: -1},
		}}}

		action, ok := findAction(deriveActions(r), "settings")
		if !ok {
			t.Fatal("expected an action for the missing table")
		}
		if action.Command != "" {
			t.Errorf("expected no command, got %q", action.Command)
		}
	})

	t.Run("it collapses new and modified files into one sync", func(t *testing.T) {
		r := Report{Entities: Entities{Files: []EntityFile{
			{Path: "a.yaml", Status: "new"},
			{Path: "b.yaml", Status: "modified"},
			{Path: "c.yaml", Status: "synced"},
		}}}

		actions := deriveActions(r)
		if len(actions) != 1 || actions[0].Command != "joka entity sync" {
			t.Fatalf("expected one entity sync action, got %+v", actions)
		}
	})

	t.Run("it does not flag an update-strategy table for differing counts", func(t *testing.T) {
		r := Report{Templates: Templates{Tables: []TemplateTable{
			{Name: "settings", Strategy: "update", Declared: 3, Live: 9},
		}}}

		if actions := deriveActions(r); len(actions) != 0 {
			t.Fatalf("expected no action for an update table, got %+v", actions)
		}
	})

	t.Run("it flags a truncate table whose count differs", func(t *testing.T) {
		r := Report{Templates: Templates{Tables: []TemplateTable{
			{Name: "settings", Strategy: "truncate", Declared: 3, Live: 9},
		}}}

		actions := deriveActions(r)
		if len(actions) != 1 || actions[0].Command != "joka data sync" {
			t.Fatalf("expected data sync, got %+v", actions)
		}
	})
}

// --- render ----------------------------------------------------------------

func TestRenderText(t *testing.T) {
	report := Report{
		Driver:  "postgres",
		Profile: "local",
		Migrations: Migrations{
			Dir:     "devops/migrations",
			Applied: 1,
			Pending: 1,
			Files: []Migration{
				{Index: "240101000000", Name: "first", Declared: true, Tracked: true, Status: MigrationApplied},
				{Index: "240102000000", Name: "second", Declared: true, Status: MigrationPending},
			},
			Drift: Drift{
				Checked:       true,
				SnapshotIndex: "240101000000",
				Added:         []string{"audit_log"},
				Modified:      []ModifiedTable{{Table: "users", OnlyInLive: []string{"email text"}}},
			},
		},
		Entities: Entities{
			Dir:    "devops/entities",
			Counts: Counts{Synced: 1, Orphaned: 1},
			Files: []EntityFile{
				{Path: "a.yaml", Status: "synced", Declared: 2, Tracked: 2, Live: 2, KeyedByID: true},
				{Path: "gone.yaml", Status: "orphaned", Declared: -1, Tracked: 1, MissingRows: 1, MissingTables: []string{"slots"}},
			},
		},
		Templates: Templates{
			Dir: "devops/templates",
			Tables: []TemplateTable{
				{Name: "settings", Strategy: "truncate", Files: 2, Declared: 3, Live: 9},
				{Name: "industry_types", Strategy: "truncate", Files: 1, Declared: 4, Live: -1, TableMissing: true},
			},
		},
		Lock: &Lock{LockedBy: "box:1", LockedAt: "2026-08-26 09:00:00", Operation: "entity sync", Age: "2h0m0s"},
	}
	report.Actions = deriveActions(report)

	var buf bytes.Buffer
	RenderText(&buf, report)
	out := buf.String()

	for _, want := range []string{
		"joka status",
		"postgres · profile local",
		"declared = the devops folder",
		"MIGRATIONS  devops/migrations",
		"240102000000_second",
		"1 applied · 1 pending",
		"schema drift vs snapshot 240101000000",
		"+ audit_log",
		"~ users",
		"live only:     email text",
		"ENTITIES  devops/entities",
		"table slots no longer exists",
		"TEMPLATES  devops/templates",
		"LOCK",
		`"entity sync" held by box:1`,
		"ACTIONS",
		"joka migrate up",
		"(nothing joka can run)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected the report to contain %q\n---\n%s", want, out)
		}
	}
}

func TestRenderTextAlignsMultiByteGlyphs(t *testing.T) {
	// The ✓ and · glyphs are three bytes each. A byte-width pad would leave
	// the columns after them ragged, so assert the columns line up.
	report := Report{
		Driver: "postgres",
		Migrations: Migrations{
			Dir: "devops/migrations",
			Files: []Migration{
				{Index: "240101000000", Name: "short", Declared: true, Tracked: true, Status: MigrationApplied},
				{Index: "240102000000", Name: "a_much_longer_name", Declared: true, Status: MigrationPending},
			},
		},
	}

	var buf bytes.Buffer
	RenderText(&buf, report)

	var starts []int
	for _, line := range strings.Split(buf.String(), "\n") {
		if !strings.Contains(line, "24010") {
			continue
		}
		// Rune offsets, not byte: ✓ is three bytes and · is two, so two aligned
		// columns sit at different byte offsets.
		starts = append(starts, textui.Width(line[:strings.Index(line, glyphPresent)]))
	}

	if len(starts) != 2 {
		t.Fatalf("expected two migration lines with a present glyph, got %d", len(starts))
	}
	if starts[0] != starts[1] {
		t.Errorf("expected the glyph column to line up, got offsets %v", starts)
	}
}

func hashOf(t *testing.T, dir, name string) string {
	t.Helper()
	h, err := entityapp.HashFileContent(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("hashing %s: %v", name, err)
	}
	return h
}

func TestBuildEntitiesIdentityProblems(t *testing.T) {
	ctx := context.Background()

	t.Run("it reports an entity with no _id against its file", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "a.yaml", "entities:\n  - _is: users\n    _id: admin\n  - _is: users\n    name: no id\n")

		e, err := buildEntities(ctx, entityInputs(dir, fakeEntities{}, fakeProbe{tables: map[string]bool{"joka_entities": true}}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		file := findEntity(t, e, "a.yaml")
		if len(file.IdentityProblems) != 1 {
			t.Fatalf("expected 1 identity problem, got %+v", file.IdentityProblems)
		}
		if file.IdentityProblems[0].Kind != entityapp.ProblemMissingID {
			t.Errorf("expected missing_id, got %q", file.IdentityProblems[0].Kind)
		}
	})

	t.Run("it reports a duplicate _id against both files", func(t *testing.T) {
		// The problem only exists across files, so it has to be attached to
		// each claimant or one of them looks clean.
		dir := t.TempDir()
		writeFile(t, dir, "a.yaml", "entities:\n  - _is: users\n    _id: admin\n")
		writeFile(t, dir, "b.yaml", "entities:\n  - _is: users\n    _id: admin\n")

		e, err := buildEntities(ctx, entityInputs(dir, fakeEntities{}, fakeProbe{tables: map[string]bool{"joka_entities": true}}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for _, path := range []string{"a.yaml", "b.yaml"} {
			file := findEntity(t, e, path)
			if len(file.IdentityProblems) != 1 {
				t.Errorf("%s: expected the duplicate reported, got %+v", path, file.IdentityProblems)
				continue
			}
			if file.IdentityProblems[0].RefID != "admin" {
				t.Errorf("%s: expected the _id named, got %q", path, file.IdentityProblems[0].RefID)
			}
		}
	})

	t.Run("it reports nothing for a valid set", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, dir, "a.yaml", "entities:\n  - _is: users\n    _id: admin\n")
		writeFile(t, dir, "b.yaml", "entities:\n  - _is: users\n    _id: other\n")

		e, err := buildEntities(ctx, entityInputs(dir, fakeEntities{}, fakeProbe{tables: map[string]bool{"joka_entities": true}}))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		for _, f := range e.Files {
			if len(f.IdentityProblems) != 0 {
				t.Errorf("%s: expected no problems, got %+v", f.Path, f.IdentityProblems)
			}
		}
	})
}

func TestIdentityActionHasNoCommand(t *testing.T) {
	// An _id has to be authored; choosing one is a decision about what the
	// entity is, so joka has nothing to run.
	r := Report{Entities: Entities{Files: []EntityFile{{
		Path:   "a.yaml",
		Status: "new",
		IdentityProblems: []entityapp.EntitySetProblem{{
			Kind:  entityapp.ProblemMissingID,
			Where: []entityapp.EntityLocation{{File: "a.yaml", Position: 2, Table: "users"}},
		}},
	}}}}

	action, ok := findAction(deriveActions(r), "a.yaml")
	if !ok {
		t.Fatal("expected an action for the file")
	}
	if action.Command != "" {
		t.Errorf("expected no command, got %q", action.Command)
	}
	if !strings.Contains(action.Reason, "without an _id") {
		t.Errorf("expected the reason to name the problem, got %q", action.Reason)
	}
}
