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
			// `joka drop` takes joka_meta with it, so this is what a wiped
			// database looks like from a checkout that synced it. It is not a
			// substitution: reading it as one left no way to rebuild a database
			// by hand.
			name:    "a file against a database with no state at all",
			hasFile: true, fileID: here, fileVersion: 3,
			want: AuditDatabaseUntracked,
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

func TestDropDoesNotLookLikeADifferentDatabase(t *testing.T) {
	// `joka drop` takes joka_meta with it, so the database has no identity and
	// the state file beside the checkout still has one. Reading that as a
	// substitution made `joka drop` followed by `joka init` refuse, which left
	// no way to rebuild a database by hand — reset only worked because its
	// steps are internal calls that never reach the gate.
	audit := AuditState(true, "file-identity", 4, "", 0)

	if audit != AuditDatabaseUntracked {
		t.Errorf("expected database_untracked, got %q", audit)
	}
	if audit.BlocksWrite() {
		t.Error("expected a wiped database to be reported, not refused")
	}
	if !audit.NeedsAttention() {
		t.Error("expected it still reported")
	}
}

func TestOnlyTwoDisagreeingIdentitiesRefuse(t *testing.T) {
	blocks := map[StateAudit]bool{
		AuditState(true, "one", 4, "two", 4): true,  // different_database
		AuditState(true, "one", 4, "one", 4): false, // agrees
		AuditState(true, "one", 5, "one", 4): false, // database_behind
		AuditState(true, "one", 3, "one", 4): false, // file_behind
		AuditState(false, "", 0, "one", 4):   false, // no_file
		AuditState(false, "", 0, "", 0):      false, // untracked
		AuditState(true, "one", 4, "", 0):    false, // database_untracked
	}

	for audit, wantBlocked := range blocks {
		if audit.BlocksWrite() != wantBlocked {
			t.Errorf("%s: expected blocked=%v", audit, wantBlocked)
		}
	}
}
