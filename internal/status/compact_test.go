package status

import (
	"bytes"
	"strings"
	"testing"
)

func compactLine(t *testing.T, r Report) string {
	t.Helper()
	r.normalize()
	r.Actions = deriveActions(r)

	var buf bytes.Buffer
	RenderCompact(&buf, r)
	return strings.TrimSpace(buf.String())
}

func TestRenderCompact(t *testing.T) {
	t.Run("it reports ok when nothing needs doing", func(t *testing.T) {
		line := compactLine(t, Report{
			Migrations: Migrations{
				Applied: 2,
				Files: []Migration{
					{Index: "1", Declared: true, Tracked: true, Status: MigrationApplied},
					{Index: "2", Declared: true, Tracked: true, Status: MigrationApplied},
				},
				Drift: Drift{Checked: true},
			},
			Entities: Entities{
				Counts: Counts{Synced: 2},
				Files: []EntityFile{
					{Path: "a.yaml", Status: "synced"},
					{Path: "b.yaml", Status: "synced"},
				},
			},
			Templates: Templates{Tables: []TemplateTable{
				{Name: "settings", Strategy: "truncate", Declared: 3, Live: 3},
			}},
		})

		want := "joka  migrations 2/2  drift 0  entities 2/2  templates 1/1  → ok"
		if line != want {
			t.Errorf("expected %q\n     got %q", want, line)
		}
	})

	t.Run("it counts what is behind and what is broken", func(t *testing.T) {
		line := compactLine(t, Report{
			Migrations: Migrations{
				Applied: 2, Pending: 1, FileMissing: 1,
				Files: []Migration{
					{Index: "1", Declared: true, Tracked: true, Status: MigrationApplied},
					{Index: "2", Declared: true, Tracked: true, Status: MigrationApplied},
					{Index: "3", Declared: true, Status: MigrationPending},
					{Index: "0", Tracked: true, Status: MigrationFileMissing},
				},
				Drift: Drift{Checked: true, Added: []string{"audit_log"}},
			},
			Entities: Entities{
				Counts: Counts{Synced: 1, Modified: 1, Orphaned: 1},
				Files: []EntityFile{
					{Path: "a.yaml", Status: "synced"},
					{Path: "b.yaml", Status: "modified"},
					{Path: "c.yaml", Status: "orphaned"},
				},
			},
			Templates: Templates{Tables: []TemplateTable{
				{Name: "settings", Strategy: "truncate", Declared: 3, Live: 9},
				{Name: "emails", Strategy: "truncate", Declared: 1, Live: 1},
			}},
		})

		// 3 declared files, 2 applied; 1 file_missing is neither declared nor
		// countable as applied, so it shows as the trailing !1.
		for _, want := range []string{
			"migrations 2/3 !1",
			"drift 1",
			"entities 1/3 +1 !1",
			"templates 1/2",
			"actions",
		} {
			if !strings.Contains(line, want) {
				t.Errorf("expected %q in %q", want, line)
			}
		}
	})

	t.Run("it counts a file with missing rows as broken even when it is synced", func(t *testing.T) {
		line := compactLine(t, Report{
			Entities: Entities{
				Counts: Counts{Synced: 1},
				Files:  []EntityFile{{Path: "a.yaml", Status: "synced", Tracked: 2, Live: 1, MissingRows: 1}},
			},
		})

		if !strings.Contains(line, "entities 1/1 !1") {
			t.Errorf("expected the missing row counted, got %q", line)
		}
	})

	t.Run("it does not count an orphan twice", func(t *testing.T) {
		// An orphan whose rows are also gone is one problem, not two.
		line := compactLine(t, Report{
			Entities: Entities{
				Counts: Counts{Orphaned: 1},
				Files:  []EntityFile{{Path: "gone.yaml", Status: "orphaned", Tracked: 1, MissingRows: 1}},
			},
		})

		if !strings.Contains(line, "entities 0/1 !1") {
			t.Errorf("expected one problem, got %q", line)
		}
	})

	t.Run("it says n/a for a section it could not read", func(t *testing.T) {
		line := compactLine(t, Report{
			Migrations: Migrations{Skipped: "joka_migrations does not exist"},
		})

		for _, want := range []string{"migrations n/a", "drift n/a", "entities n/a", "templates n/a"} {
			if !strings.Contains(line, want) {
				t.Errorf("expected %q in %q", want, line)
			}
		}
	})

	t.Run("it shows a held lock", func(t *testing.T) {
		line := compactLine(t, Report{
			Lock: &Lock{Operation: "entity sync", LockedBy: "box:1", Age: "2h"},
		})

		if !strings.Contains(line, "lock held") {
			t.Errorf("expected the lock reported, got %q", line)
		}
	})

	t.Run("it does not flag an update-strategy table for differing counts", func(t *testing.T) {
		line := compactLine(t, Report{
			Templates: Templates{Tables: []TemplateTable{
				{Name: "settings", Strategy: "update", Declared: 3, Live: 9},
			}},
		})

		if !strings.Contains(line, "templates 1/1") {
			t.Errorf("expected the update table counted as agreeing, got %q", line)
		}
	})
}
