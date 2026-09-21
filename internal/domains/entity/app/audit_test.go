package app

import "testing"

func TestAuditState(t *testing.T) {
	const here = "db-aaaa"
	const elsewhere = "db-bbbb"

	for _, tc := range []struct {
		name        string
		hasFile     bool
		fileID      string
		fileVersion int
		dbID        string
		dbVersion   int
		want        StateAudit
	}{
		{
			name: "a database nobody has synced has nothing to audit",
			want: AuditUntracked,
		},
		{
			name:    "a file against a database with no state at all",
			hasFile: true, fileID: here, fileVersion: 3,
			want: AuditDifferentDatabase,
		},
		{
			name:      "synced from somewhere else, so there is no file here",
			dbID:      here,
			dbVersion: 7,
			want:      AuditNoFile,
		},
		{
			name:    "the normal case",
			hasFile: true, fileID: here, fileVersion: 7,
			dbID: here, dbVersion: 7,
			want: AuditAgrees,
		},
		{
			name:    "the database was restored from an older dump",
			hasFile: true, fileID: here, fileVersion: 7,
			dbID: here, dbVersion: 4,
			want: AuditDatabaseBehind,
		},
		{
			name:    "a sync ran from another directory",
			hasFile: true, fileID: here, fileVersion: 4,
			dbID: here, dbVersion: 7,
			want: AuditFileBehind,
		},
		{
			name:    "DATABASE_URL points somewhere unintended",
			hasFile: true, fileID: elsewhere, fileVersion: 7,
			dbID: here, dbVersion: 7,
			want: AuditDifferentDatabase,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := AuditState(tc.hasFile, tc.fileID, tc.fileVersion, tc.dbID, tc.dbVersion)
			if got != tc.want {
				t.Errorf("expected %q, got %q", tc.want, got)
			}
			if got.Describe() == string(got) {
				t.Errorf("expected %q to have a sentence", got)
			}
		})
	}
}

func TestAuditNeedsAttention(t *testing.T) {
	quiet := []StateAudit{AuditAgrees, AuditUntracked}
	for _, a := range quiet {
		if a.NeedsAttention() {
			t.Errorf("expected %q to be quiet", a)
		}
	}

	loud := []StateAudit{AuditNoFile, AuditDatabaseBehind, AuditFileBehind, AuditDifferentDatabase}
	for _, a := range loud {
		if !a.NeedsAttention() {
			t.Errorf("expected %q to want attention", a)
		}
	}
}
