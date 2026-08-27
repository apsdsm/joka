package status

import (
	"os"
	"testing"
)

// TestDemoRender is a visual check: run with -v to see the layout.
func TestDemoRender(t *testing.T) {
	if os.Getenv("JOKA_STATUS_DEMO") == "" {
		t.Skip("set JOKA_STATUS_DEMO=1 to print a sample report")
	}

	report := Report{
		Driver:  "postgres",
		Profile: "dev-remote",
		Migrations: Migrations{
			Dir:     "devops/migrations",
			Applied: 3,
			Pending: 1,
			Files: []Migration{
				{Index: "250116140000", Name: "add_users", Declared: true, Tracked: true, Status: MigrationApplied},
				{Index: "250203091200", Name: "add_fields", Declared: true, Tracked: true, Status: MigrationApplied},
				{Index: "250811103000", Name: "drop_slots", Declared: true, Tracked: true, Status: MigrationApplied},
				{Index: "250826094500", Name: "add_ceo_field", Declared: true, Status: MigrationPending},
			},
			Drift: Drift{
				Checked:       true,
				SnapshotIndex: "250811103000",
				Added:         []string{"audit_log"},
				Modified: []ModifiedTable{{
					Table:      "fields",
					OnlyInLive: []string{`"label_ja" character varying(255)`},
				}},
			},
		},
		Entities: Entities{
			Dir:    "devops/entities",
			Counts: Counts{Synced: 1, Modified: 1, New: 1, Orphaned: 1},
			Files: []EntityFile{
				{Path: "01_clients/jjc2_admin.yaml", Status: "synced", Declared: 4, Tracked: 4, Live: 4, KeyedByID: true},
				{
					Path: "04_fields/system_fields.yaml", Status: "modified",
					Declared: 48, Tracked: 38, Live: 38, KeyedByID: true,
					Structural: "entity file changed structurally; use 'entity reimport': 04_fields/system_fields.yaml now defines 48 entities but 38 are tracked (an entity was added or removed)",
				},
				{Path: "08_mappings/system_mappings.yaml", Status: "new", Declared: 12},
				{
					Path: "08_slots/system_assignments.yaml", Status: "orphaned", Declared: -1,
					Tracked: 12, MissingRows: 12, MissingTables: []string{"entity_slot_assignments"},
				},
			},
		},
		Templates: Templates{
			Dir: "devops/templates",
			Tables: []TemplateTable{
				{Name: "email_templates", Strategy: "truncate", Files: 3, Declared: 3, Live: 3},
				{Name: "settings", Strategy: "truncate", Files: 2, Declared: 12, Live: 9},
				{Name: "industry_types", Strategy: "update", Files: 1, Declared: 4, Live: 11},
			},
		},
		Lock: &Lock{LockedBy: "box:31174", LockedAt: "2026-08-26 09:11:02", Operation: "entity sync", Age: "2h14m3s"},
	}
	report.Actions = deriveActions(report)

	RenderText(os.Stdout, report)
}
