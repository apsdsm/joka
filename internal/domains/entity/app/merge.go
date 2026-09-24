package app

// ColumnVerdict is what comparing one column across the three states concluded.
type ColumnVerdict string

const (
	// VerdictUnchanged: the declaration and the database already agree.
	VerdictUnchanged ColumnVerdict = "unchanged"
	// VerdictPush: the declaration moved and the database did not, so writing
	// the declared value loses nothing.
	VerdictPush ColumnVerdict = "push"
	// VerdictConflict: the database moved. Writing the declared value would
	// discard a change joka did not make, so a human decides.
	VerdictConflict ColumnVerdict = "conflict"
)

// ClassifyColumn compares a declared value, the value in the database and the
// baseline joka recorded when it last wrote the column.
//
// Two points can only say that the declaration and the database differ. The
// baseline is what makes it possible to say *which side moved*, and that is the
// difference between a prompt on every difference and a prompt only when there
// is a decision to make:
//
//	declared vs baseline | live vs baseline | verdict
//	same                 | same             | unchanged
//	changed              | same             | push — the file moved
//	same                 | changed          | conflict — the database moved
//	changed              | changed          | conflict — both moved
//
// A row with no baseline — written before joka recorded one, or by a reimport
// that could not resolve one — reads as push rather than conflict. The absence
// of a record is not evidence that the database moved, and treating it as one
// would make the first sync after an upgrade a wall of conflicts on a database
// where nothing is wrong. Push is what joka did before the baseline existed, so
// an un-baselined row behaves exactly as it always has, and the baseline is
// recorded on the way through.
func ClassifyColumn(declared, live any, baseline string, hasBaseline bool) ColumnVerdict {
	declaredHash := HashValue(declared)
	liveHash := HashValue(live)

	if declaredHash == liveHash {
		return VerdictUnchanged
	}

	if !hasBaseline {
		return VerdictPush
	}

	if liveHash == baseline {
		return VerdictPush
	}

	return VerdictConflict
}
