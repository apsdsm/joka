package status

import (
	"fmt"

	entityapp "github.com/apsdsm/joka/internal/domains/entity/app"
	entitydomain "github.com/apsdsm/joka/internal/domains/entity/domain"
	templatedomain "github.com/apsdsm/joka/internal/domains/template/domain"
)

// deriveActions turns the findings into the commands that resolve them.
//
// Diagnosis without a remedy is what the existing output already gives: the
// reader has to know that a hash mismatch means `entity sync`, that a
// structural change means `entity reimport`, and that an orphan means a manual
// DELETE. The actions list names it, and names nothing when joka genuinely has
// no command for the finding — an orphaned entity file being the current
// example.
func deriveActions(r Report) []Action {
	var actions []Action

	add := func(scope, subject, reason, command string) {
		actions = append(actions, Action{
			Scope:   scope,
			Subject: subject,
			Reason:  reason,
			Command: command,
		})
	}

	m := r.Migrations

	if m.Skipped != "" && len(m.Files) == 0 {
		add(ScopeMigrations, "", m.Skipped, "joka init")
	}

	if m.Pending > 0 {
		add(ScopeMigrations, "", plural(m.Pending, "pending migration", "pending migrations"), "joka migrate up")
	}

	for _, file := range m.Files {
		switch file.Status {
		case MigrationOutOfOrder:
			add(ScopeMigrations, file.Index,
				"a file that sorts before an applied migration is not applied; migrate up applies in order and will not reach it",
				"")
		case MigrationFileMissing:
			add(ScopeMigrations, file.Index,
				"applied but no file on disk; migrate status and migrate up both fail on this",
				"")
		}
	}

	if m.Drift.HasDrift() {
		add(ScopeMigrations, m.Drift.SnapshotIndex,
			fmt.Sprintf("%s differ from the snapshot; run verify for the full statements, or snapshot to re-capture if the change is intended",
				plural(m.Drift.Count(), "table", "tables")),
			"joka migrate verify")
	}

	needsSync := 0

	for _, file := range r.Entities.Files {
		switch {
		case len(file.IdentityProblems) > 0:
			// No command fixes this: an _id has to be authored, and choosing
			// one is a decision about what the entity is.
			add(ScopeEntities, file.Path, identityReason(file.IdentityProblems), "")

		case file.ParseError != "":
			add(ScopeEntities, file.Path, "cannot be parsed: "+file.ParseError, "")

		case file.Status == string(entitydomain.StatusOrphaned):
			add(ScopeEntities, file.Path,
				fmt.Sprintf("tracked with %s but the file is gone; forget drops the tracking and leaves any surviving rows alone",
					plural(file.Tracked, "row", "rows")),
				"joka entity forget "+file.Path)

		case file.Structural != "":
			add(ScopeEntities, file.Path, file.Structural, "joka entity reimport "+file.Path)

		// The file still declares these rows, so restoring them is the action
		// that makes the database match the devops folder. Forget is the
		// answer when the deletion was deliberate, which is a decision only
		// the reader can make.
		case file.MissingRows > 0:
			add(ScopeEntities, file.Path,
				fmt.Sprintf("%s tracked for this file %s no longer in the database; reimport re-creates them, 'joka entity forget %s' drops the tracking instead",
					plural(file.MissingRows, "row", "rows"), isAre(file.MissingRows), file.Path),
				"joka entity reimport "+file.Path)

		case file.Status == string(entitydomain.StatusNew), file.Status == string(entitydomain.StatusModified):
			needsSync++
		}
	}

	if needsSync > 0 {
		add(ScopeEntities, "", plural(needsSync, "file is new or modified", "files are new or modified"), "joka entity sync")
	}

	staleTables := 0

	for _, table := range r.Templates.Tables {
		switch {
		case table.TableMissing:
			add(ScopeTemplates, table.Name,
				"the table does not exist; a migration should create it before data sync runs",
				"")

		case table.LoadError != "":
			add(ScopeTemplates, table.Name, "records cannot be read: "+table.LoadError, "")

		// Only truncate makes the two counts equal by definition. An update
		// table holding extra rows is not a finding.
		case table.Strategy == string(templatedomain.StrategyTruncate) && table.Declared != table.Live:
			staleTables++
		}
	}

	if staleTables > 0 {
		add(ScopeTemplates, "",
			fmt.Sprintf("%s a different number of rows than the files declare",
				plural(staleTables, "truncate table holds", "truncate tables hold")),
			"joka data sync")
	}

	if r.Lock != nil {
		add(ScopeLock, "",
			fmt.Sprintf("%q has held the lock for %s (by %s); unlock only if no joka process is running",
				r.Lock.Operation, r.Lock.Age, r.Lock.LockedBy),
			"joka unlock")
	}

	return actions
}

// plural renders a count with the singular or plural form of a noun phrase.
func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %s", n, pluralForm)
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// identityReason summarises why a file is not ready for identity-keyed
// tracking. joka has no command for it — an _id has to be authored.
func identityReason(problems []entityapp.EntitySetProblem) string {
	missing, duplicate := 0, 0
	for _, p := range problems {
		if p.Kind == entityapp.ProblemMissingID {
			missing++
			continue
		}
		duplicate++
	}

	switch {
	case missing > 0 && duplicate > 0:
		return fmt.Sprintf("%s without an _id and %s claimed by another file; joka identifies every seeded row by _id",
			plural(missing, "entity", "entities"), plural(duplicate, "_id", "_ids"))
	case missing > 0:
		return fmt.Sprintf("%s without an _id; joka identifies every seeded row by _id",
			plural(missing, "entity", "entities"))
	default:
		return fmt.Sprintf("%s also claimed by another file; an _id identifies one row, so it can only be claimed once",
			plural(duplicate, "_id", "_ids"))
	}
}
