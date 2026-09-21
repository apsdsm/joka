package app

// StateAudit is what comparing the state file against the database's own
// markers concluded.
//
// A copy of the state outside the database is not there for durability — the
// database backend is transactional and the file is not. It is there because
// state that lives inside the thing it describes is always self-consistent, so
// it can never report that this is the wrong database, or the right one
// restored from an older dump. These are the cases only two copies can tell
// apart.
type StateAudit string

const (
	// AuditAgrees: the file and the database name the same database at the
	// same version.
	AuditAgrees StateAudit = "agrees"
	// AuditNoFile: nothing has been written here. Normal on a database nobody
	// has synced from this directory, and a finding on one that has.
	AuditNoFile StateAudit = "no_file"
	// AuditUntracked: the database has never had state written to it, so there
	// is nothing for a file to describe.
	AuditUntracked StateAudit = "untracked"
	// AuditDatabaseBehind: same database, lower version than the file records.
	// It was restored from a dump, or rolled back. joka's own tracking cannot
	// see this, because it rolled back too.
	AuditDatabaseBehind StateAudit = "database_behind"
	// AuditFileBehind: same database, higher version than the file records.
	// Something wrote state and the file was not materialized — a crash
	// between the commit and the write, or a sync run from another directory.
	AuditFileBehind StateAudit = "file_behind"
	// AuditDifferentDatabase: the file describes a database this is not.
	// DATABASE_URL points somewhere unintended.
	AuditDifferentDatabase StateAudit = "different_database"
)

// AuditState compares a state file's markers against the database's.
//
// fileIdentity and fileVersion come from the file; dbIdentity and dbVersion
// from joka_meta. hasFile is false when there is no file to compare.
func AuditState(hasFile bool, fileIdentity string, fileVersion int, dbIdentity string, dbVersion int) StateAudit {
	// Nothing has ever written state to this database, so there is nothing for
	// a file to be right or wrong about.
	if dbIdentity == "" && dbVersion == 0 {
		if hasFile {
			return AuditDifferentDatabase
		}
		return AuditUntracked
	}

	if !hasFile {
		return AuditNoFile
	}

	if fileIdentity != dbIdentity {
		return AuditDifferentDatabase
	}

	switch {
	case fileVersion == dbVersion:
		return AuditAgrees
	case fileVersion > dbVersion:
		return AuditDatabaseBehind
	default:
		return AuditFileBehind
	}
}

// Describe renders the audit as a sentence for a report.
func (a StateAudit) Describe() string {
	switch a {
	case AuditAgrees:
		return "the state file and the database agree"
	case AuditNoFile:
		return "no state file here — this database has been synced, but not from this directory"
	case AuditUntracked:
		return "no state has been written to this database"
	case AuditDatabaseBehind:
		return "the database is behind the state file — it was restored from a dump, or rolled back"
	case AuditFileBehind:
		return "the state file is behind the database — a sync ran from somewhere else, or did not finish writing"
	case AuditDifferentDatabase:
		return "the state file describes a different database"
	}
	return string(a)
}

// NeedsAttention reports whether the audit found something worth acting on.
func (a StateAudit) NeedsAttention() bool {
	switch a {
	case AuditAgrees, AuditUntracked:
		return false
	}
	return true
}

// BlocksWrite reports whether a command that writes should refuse on this
// verdict.
//
// Only a disagreeing identity blocks. A version disagreement is informational:
// joka loads what it applies from the database, so a file that is ahead or
// behind does not change what the run does, and refusing would strand anyone
// whose last sync was interrupted after the commit. A disagreeing identity
// means the file describes a database this is not, and that is worth stopping
// for — adoption claims the rows it finds rather than colliding with them, so
// the wrong database no longer announces itself with a duplicate key.
func (a StateAudit) BlocksWrite() bool {
	return a == AuditDifferentDatabase
}
