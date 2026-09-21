package app

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"time"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// How a declared entity and a tracked row were paired up.
const (
	// MatchByID aligned the two sides on _id / ref_id. Available only when
	// every entity and every tracked row carries one.
	MatchByID = "id"
	// MatchByPosition paired entity #N with tracked row #N. It is the fallback
	// for a set sync would refuse — one where an _id is missing on either side
	// — and it is what makes an inserted entity look like a rename of
	// everything after it.
	//
	// Sync itself no longer matches this way. It did until `0c4e64d`, which is
	// why the fallback exists at all: a database synced before that, or a file
	// that has not yet been given _ids, still has to be describable.
	MatchByPosition = "position"
)

// What happened to one line of the alignment.
const (
	DiffSame     = "same"     // paired, no column changes
	DiffChanged  = "changed"  // paired, some columns differ
	DiffInsert   = "insert"   // declared but not tracked
	DiffDelete   = "delete"   // tracked but not declared
	DiffUnpaired = "unpaired" // paired, but the two sides name different tables
)

// EntityDiff aligns an entity file's declared graph against the rows joka
// tracks for it, and against the rows that are actually in the database.
//
// It exists because the only thing that reports this today is sync's refusal:
// "now defines 48 entities but 38 are tracked". That names the symptom without
// saying which entities are new or where they were inserted, and the remedy it
// suggests (reimport) deletes every row the file owns. This shows the shape of
// the change instead, and says whether an _id-keyed match would be exact.
type EntityDiff struct {
	Path string `json:"file"`
	// Tracked is false for a file that has never been synced; every line is
	// then an insert.
	Tracked bool `json:"tracked"`
	// OnDisk is false for an orphan; every line is then a delete.
	OnDisk bool `json:"on_disk"`

	MatchedBy string     `json:"matched_by"`
	Lines     []DiffLine `json:"lines"`

	DeclaredCount int `json:"declared_count"`
	TrackedCount  int `json:"tracked_count"`
	Inserts       int `json:"inserts"`
	Deletes       int `json:"deletes"`
	Changes       int `json:"changes"`
	Moves         int `json:"moves"`
	// RegeneratedColumns are the columns whose value is rewritten on every
	// sync because they come from a non-deterministic template ({{ now }},
	// {{ argon2id|… }}, an asm.* secret). They are reported once for the file
	// rather than against every row, and they are not counted as changes: they
	// are a property of the file, not a difference from the database.
	RegeneratedColumns []string `json:"regenerated_columns"`

	// SeededColumns are the columns declared `_once`: joka set them when it
	// inserted the row and the database owns them from then on. Like
	// RegeneratedColumns they are a property of the file rather than a
	// difference from the database — sync will not write them, so whatever the
	// row holds is not something joka is about to change.
	SeededColumns []string `json:"seeded_columns"`

	// MissingRows counts tracked rows that are no longer in the database.
	MissingRows int `json:"missing_rows"`

	// KeyedByID reports whether every declared entity and every tracked row
	// carries an _id — the precondition for matching on identity rather than
	// position.
	KeyedByID bool `json:"keyed_by_id"`
	// UnkeyedDeclared and UnkeyedTracked name what is stopping an _id match.
	UnkeyedDeclared []string `json:"unkeyed_declared"`
	UnkeyedTracked  []string `json:"unkeyed_tracked"`

	// PositionalBreak is the 1-based declared position where positional
	// alignment first stops describing the same row, or 0 when it holds all
	// the way through.
	//
	// It is diagnostic only: it says how far the file has drifted from the
	// order its rows were inserted in. When sync matched positionally it was
	// also the position sync would start writing to the wrong row; it has not
	// meant that since `0c4e64d`.
	PositionalBreak int `json:"positional_break"`

}

// DiffLine is one row of the alignment: a declared entity, a tracked row, or
// both.
type DiffLine struct {
	Status string `json:"status"`

	// Depth is the entity's nesting level in the file: 0 for a top-level
	// entity, 1 for one under a `_has:`, and so on. 0 for a line with no
	// declared side — a tracked row records no nesting.
	Depth int `json:"depth"`

	// DeclaredPos is the 1-based depth-first position in the file, or 0 when
	// the line has no declared side.
	DeclaredPos int `json:"declared_pos"`
	// TrackedPos is the 1-based insertion order, or 0 when the line has no
	// tracked side.
	TrackedPos int `json:"tracked_pos"`

	Table string `json:"table"`
	RefID string `json:"ref_id,omitempty"`
	// Moved is true when the two sides were paired but sit at different
	// positions — an insert earlier in the file pushes everything after it
	// down, which is exactly what breaks positional matching.
	Moved bool `json:"moved"`

	// DeclaredTable is set only when it disagrees with the tracked row's
	// table, which positional matching can produce.
	DeclaredTable string `json:"declared_table,omitempty"`

	PKColumn string `json:"pk_column,omitempty"`
	PKValue  int64  `json:"pk_value,omitempty"`
	// Live reports whether the tracked row is still in the database.
	Live bool `json:"live"`
	// TableMissing reports that the tracked row's table no longer exists.
	TableMissing bool `json:"table_missing"`

	// Changes are the columns that differ between the file and the live row.
	// Only computed for a paired line whose row is live.
	Changes []ColumnChange `json:"changes,omitempty"`
	// ChangesSkipped explains why Changes is empty on a paired line that was
	// not compared.
	ChangesSkipped string `json:"changes_skipped,omitempty"`
}

// DiffEntityAction builds the alignment for one entity file.
type DiffEntityAction struct {
	DB DBAdapter
	// State is what joka last applied. The tracked side of the alignment comes
	// from it; DB is only used for the live side (does the row still exist,
	// what does it hold).
	State *domain.State
	// Path is the relative path used as the tracking key.
	Path string
	// Entities is the parsed file's graph. Nil for an orphan.
	Entities []domain.Entity
	// OnDisk is false when the file no longer exists.
	OnDisk bool
	// SkipValues turns off the per-row column comparison. It is on by default
	// because a structural diff that stays silent about an edited label is
	// only half the picture; the cost is one query per paired row.

	SkipValues bool
}

// Execute builds the diff. It is read-only.
func (a DiffEntityAction) Execute(ctx context.Context) (*EntityDiff, error) {
	diff := &EntityDiff{Path: a.Path, OnDisk: a.OnDisk}

	declared := flattenEntities(a.Entities, nil)
	depths := flattenDepths(a.Entities, 0, nil)
	diff.DeclaredCount = len(declared)

	_, synced := a.State.FileHash(a.Path)
	diff.Tracked = synced

	var tracked []domain.TrackedRow
	if synced {
		// Already in the order the rows were written, which is the order the
		// alignment walks.
		tracked = a.State.RowsInFile(a.Path)
	}
	diff.TrackedCount = len(tracked)

	diff.UnkeyedDeclared, diff.UnkeyedTracked = unkeyed(declared, tracked)
	diff.KeyedByID = len(diff.UnkeyedDeclared) == 0 && len(diff.UnkeyedTracked) == 0 &&
		len(declared) > 0 && len(tracked) > 0

	pairs := alignPositionally(declared, tracked)
	// Positional alignment only means something when there is something on
	// both sides; an untracked file has nothing to have broken against.
	if len(declared) > 0 && len(tracked) > 0 {
		diff.PositionalBreak = positionalBreak(declared, tracked)
	}

	if diff.KeyedByID {
		pairs = alignByRefID(declared, tracked)
		diff.MatchedBy = MatchByID
	} else {
		diff.MatchedBy = MatchByPosition
	}

	if err := a.buildLines(ctx, diff, declared, depths, tracked, pairs); err != nil {
		return nil, err
	}

	return diff, nil
}

// pair is one line of the alignment before liveness and column changes are
// filled in. Either index may be -1.
type pair struct {
	declared int
	tracked  int
}

func (a DiffEntityAction) buildLines(ctx context.Context, diff *EntityDiff, declared []domain.Entity, depths []int, tracked []domain.TrackedRow, pairs []pair) error {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	refMap := make(map[string]int64)
	tableExists := make(map[string]bool)
	regenerated := make(map[string]struct{})

	for _, p := range pairs {
		line := DiffLine{}

		var entity *domain.Entity
		if p.declared >= 0 {
			entity = &declared[p.declared]
			line.DeclaredPos = p.declared + 1
			if p.declared < len(depths) {
				line.Depth = depths[p.declared]
			}
			line.Table = entity.Table
			line.RefID = entity.RefID
		}

		var row *domain.TrackedRow
		if p.tracked >= 0 {
			row = &tracked[p.tracked]
			line.TrackedPos = p.tracked + 1
			line.PKColumn = row.PKColumn
			if line.PKColumn == "" {
				line.PKColumn = "id"
			}
			line.PKValue = row.RowPK
			if line.Table == "" {
				line.Table = row.TableName
			}
			if line.RefID == "" {
				line.RefID = row.RefID
			}
		}

		switch {
		case entity == nil:
			line.Status = DiffDelete
			diff.Deletes++
		case row == nil:
			line.Status = DiffInsert
			diff.Inserts++
		default:
			line.Status = DiffSame
			// Positional alignment can pair rows in different tables. Say so
			// rather than pretending they describe the same thing.
			if entity.Table != row.TableName {
				line.DeclaredTable = entity.Table
				line.Table = row.TableName
				line.Status = DiffUnpaired
			}
			// Moved is orthogonal to whether the values changed: an insert
			// earlier in the file shifts every row after it, and those rows
			// may or may not also have been edited.
			if line.DeclaredPos != line.TrackedPos {
				line.Moved = true
				diff.Moves++
			}
		}

		if row != nil {
			live, err := a.rowIsLive(ctx, tableExists, *row, line.PKColumn)
			if err != nil {
				return err
			}
			line.Live = live.live
			line.TableMissing = live.tableMissing
			if !live.live {
				diff.MissingRows++
			}
		}

		if entity != nil && row != nil && line.Live && line.Status != DiffUnpaired {
			if err := a.compareValues(ctx, &line, *entity, refMap, now); err != nil {
				return err
			}

			// A regenerated column is a property of the file, not a difference
			// from the database: it would be rewritten on every sync whatever
			// the row holds. Collect it once for the file instead of marking
			// every row changed, which on a file with a {{ now }} column means
			// all of them.
			var real []ColumnChange
			for _, change := range line.Changes {
				if change.Regenerated {
					regenerated[change.Column] = struct{}{}
					continue
				}
				real = append(real, change)
			}
			line.Changes = real

			if len(real) > 0 {
				if line.Status == DiffSame {
					line.Status = DiffChanged
				}
				diff.Changes++
			}
		} else if entity != nil && row != nil {
			line.ChangesSkipped = "row is not in the database"
			if line.Status == DiffUnpaired {
				line.ChangesSkipped = "paired rows are in different tables"
			}
		}

		if entity != nil && row != nil && entity.RefID != "" {
			refMap[entity.RefID] = row.RowPK
		}

		diff.Lines = append(diff.Lines, line)
	}

	for col := range regenerated {
		diff.RegeneratedColumns = append(diff.RegeneratedColumns, col)
	}
	sort.Strings(diff.RegeneratedColumns)

	seeded := make(map[string]struct{})
	for _, e := range declared {
		for _, col := range e.Once {
			seeded[col] = struct{}{}
		}
	}
	for col := range seeded {
		diff.SeededColumns = append(diff.SeededColumns, col)
	}
	sort.Strings(diff.SeededColumns)

	return nil
}

type liveness struct {
	live         bool
	tableMissing bool
}

func (a DiffEntityAction) rowIsLive(ctx context.Context, cache map[string]bool, row domain.TrackedRow, pkColumn string) (liveness, error) {
	exists, cached := cache[row.TableName]
	if !cached {
		var err error
		exists, err = a.DB.TableExists(ctx, row.TableName)
		if err != nil {
			return liveness{}, err
		}
		cache[row.TableName] = exists
	}
	if !exists {
		return liveness{tableMissing: true}, nil
	}

	live, err := a.DB.RowExists(ctx, row.TableName, pkColumn, row.RowPK)
	if err != nil {
		return liveness{}, err
	}
	return liveness{live: live}, nil
}

func (a DiffEntityAction) compareValues(ctx context.Context, line *DiffLine, entity domain.Entity, refMap map[string]int64, now string) error {
	if a.SkipValues {
		line.ChangesSkipped = "not compared (--no-values)"
		return nil
	}

	// The diff reports what differs, so it asks the comparison to treat the
	// file as changed: a non-deterministic column is a difference it should
	// name, not one it should stay quiet about because this run would not
	// write it.
	row, tracked := a.State.Row(entity.RefID)
	if !tracked {
		row = domain.EntityState{Table: entity.Table, PKColumn: line.PKColumn, PKValue: line.PKValue}
	}

	changes, err := ResolveRowChanges(ctx, a.DB, entity, row, refMap, now, true)
	if err != nil {
		// A value comparison that fails is worth reporting against the line
		// rather than failing the whole diff — the structural picture is the
		// reason the command was run.
		if errors.Is(err, domain.ErrInvalidReference) {
			line.ChangesSkipped = "depends on a reference this diff cannot resolve"
			return nil
		}
		return err
	}

	line.Changes = changes
	return nil
}

// alignByRefID pairs the two sides on _id, preserving declared order and
// appending anything tracked that the file no longer declares.
func alignByRefID(declared []domain.Entity, tracked []domain.TrackedRow) []pair {
	byRef := make(map[string]int, len(tracked))
	for i, row := range tracked {
		byRef[row.RefID] = i
	}

	used := make([]bool, len(tracked))
	pairs := make([]pair, 0, len(declared)+len(tracked))

	for i, e := range declared {
		j, ok := byRef[e.RefID]
		if !ok {
			pairs = append(pairs, pair{declared: i, tracked: -1})
			continue
		}
		used[j] = true
		pairs = append(pairs, pair{declared: i, tracked: j})
	}

	for j := range tracked {
		if !used[j] {
			pairs = append(pairs, pair{declared: -1, tracked: j})
		}
	}

	return pairs
}

// alignPositionally pairs entity #N with tracked row #N — what sync does — and
// leaves the excess on whichever side is longer unpaired.
func alignPositionally(declared []domain.Entity, tracked []domain.TrackedRow) []pair {
	n := max(len(declared), len(tracked))
	pairs := make([]pair, 0, n)

	for i := range n {
		p := pair{declared: -1, tracked: -1}
		if i < len(declared) {
			p.declared = i
		}
		if i < len(tracked) {
			p.tracked = i
		}
		pairs = append(pairs, p)
	}

	return pairs
}

// positionalBreak returns the 1-based position where walking both sides in
// step first stops describing the same row, or 0 when it never does. A
// differing table or _id at the same index is the break; so is one side simply
// running out.
func positionalBreak(declared []domain.Entity, tracked []domain.TrackedRow) int {
	n := min(len(declared), len(tracked))

	for i := range n {
		if declared[i].Table != tracked[i].TableName {
			return i + 1
		}
		if declared[i].RefID != "" && tracked[i].RefID != "" && declared[i].RefID != tracked[i].RefID {
			return i + 1
		}
	}

	if len(declared) != len(tracked) {
		return n + 1
	}

	return 0
}

// unkeyed returns the entities and tracked rows with no _id, described well
// enough to find them. These are what stop an identity match.
func unkeyed(declared []domain.Entity, tracked []domain.TrackedRow) (declaredOut, trackedOut []string) {
	for i, e := range declared {
		if e.RefID == "" {
			declaredOut = append(declaredOut, positionLabel(i+1, e.Table))
		}
	}
	for i, row := range tracked {
		if row.RefID == "" {
			trackedOut = append(trackedOut, positionLabel(i+1, row.TableName))
		}
	}
	return declaredOut, trackedOut
}

func positionLabel(pos int, table string) string {
	return "#" + strconv.Itoa(pos) + " " + table
}


// CountEntities returns the number of entities in a graph, children included.
// The count is order-independent, so callers that only need a size do not have
// to flatten depth-first.
func CountEntities(entities []domain.Entity) int {
	n := 0
	for _, e := range entities {
		n += 1 + CountEntities(e.Children)
	}
	return n
}

// flattenDepths returns the nesting depth of each entity in the same
// depth-first pre-order that flattenEntities produces, so element i of each
// describes the same entity. Top-level entities are depth 0, entities under a
// `_has:` are depth 1, and so on.
//
// The graph is flattened because that is how rows are inserted and tracked, but
// the nesting is what the file actually says — a field and its version are one
// thing in the YAML, and a diff that lists them as two unrelated rows is harder
// to read than it needs to be.
func flattenDepths(entities []domain.Entity, depth int, out []int) []int {
	for _, e := range entities {
		out = append(out, depth)
		out = flattenDepths(e.Children, depth+1, out)
	}
	return out
}
