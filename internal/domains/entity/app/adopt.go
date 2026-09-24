package app

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// Adoption is a declared entity matched to a row joka did not insert.
//
// It is what makes `entity sync` usable against a database that already holds
// the data. Without it, an entity with no tracked row is an insert, and an
// insert of a row that is already there fails on whatever unique constraint the
// schema has — which is how every existing joka user's first run under the _id
// model ended. tic_main failed on `api_keys_xid_key`.
//
// The row is claimed, not merged with: its baseline is set to what it holds at
// the moment of the claim, so every column the declaration disagrees about reads
// as a push and is written over. That is the destructive reading, and it is the
// right one — the seed files are the desired state, and a row joka is being told
// to own should end up saying what they say.
//
// Recording that baseline rather than leaving it nil is what makes drift
// detectable afterwards. See liveBaseline.
type Adoption struct {
	// RefID is the entity's _id.
	RefID string
	// File is where it is declared.
	File string
	// Row is the tracking joka will record for it once the run commits.
	Row domain.EntityState
	// MatchedOn are the columns the row was found by, for the report. Someone
	// reading "joka claimed 18 rows it did not insert" needs to see what it
	// matched on before they believe it.
	MatchedOn []string
}

// adopt looks for the row a declared entity already corresponds to.
//
// It returns false when there is none, which is the ordinary case of a new
// entity on a database that does not have it yet, and of a table with no unique
// index the entity declares (so joka cannot tell, and inserting is what it has
// always done) — the caller inserts.
//
// Matching uses only literal declared values. A template resolves to something
// joka cannot predict ({{ now }}, {{ argon2id|… }}) or to a primary key that may
// not exist yet ({{ ref.id }}), so a key with a template in it is not a key that
// can identify an existing row, and the index is skipped. Matching on the
// declared values in general would be worse still: the case adoption exists for
// is the row that is there and *differs*, so a match on everything would miss
// exactly the rows it is meant to find.
func adopt(
	ctx context.Context,
	keys *keyCache,
	e domain.Entity,
	file string,
	order int,
) (Adoption, bool, error) {
	db := keys.db

	candidates, err := keys.of(ctx, e.Table)
	if err != nil {
		return Adoption{}, false, err
	}

	for _, columns := range candidates {
		key, usable := literalKey(e, columns)
		if !usable {
			continue
		}

		pk, err := db.FindByUniqueKey(ctx, e.Table, e.PKColumn, key)
		if errors.Is(err, domain.ErrRowNotFound) {
			continue
		}
		if err != nil {
			return Adoption{}, false, err
		}

		// The baseline is what the row holds at the moment joka takes ownership
		// of it.
		//
		// A nil baseline would read as push, which gets the claim itself right —
		// the declaration is written over the row — but leaves every column that
		// already agreed with no baseline at all, for ever. Drift on those is
		// then invisible: no third point, so live-versus-baseline cannot be
		// asked, and the next hand-edit is silently overwritten. Adopting an
		// existing product left five of tic_main's six entities in exactly that
		// state, which is the one case adoption exists for.
		//
		// Recording the live values does not change what the claim does. A
		// column that differs still pushes: declared moved from the baseline,
		// live has not, which is the push row of the table. A column that agrees
		// is unchanged, and now has a baseline to be measured against later.
		baseline, err := liveBaseline(ctx, db, e, pk)
		if err != nil {
			return Adoption{}, false, err
		}

		return Adoption{
			RefID:     e.RefID,
			File:      file,
			MatchedOn: columns,
			Row: domain.EntityState{
				Table:    e.Table,
				PKColumn: e.PKColumn,
				PKValue:  pk,
				File:     file,
				Order:    order,
				Columns:  baseline,
			},
		}, true, nil
	}

	return Adoption{}, false, nil
}

// keyCache remembers each table's unique indexes for the length of one run.
//
// Without it `UniqueKeys` is asked once per entity rather than once per table,
// and the ratio is not small: jjc2's shape is 294 entities over 18 tables, so
// 276 of the 294 catalog queries were asking a question already answered. That
// was 23% of every round trip a first adoption made.
//
// A run's lifetime is the right scope. The set of unique indexes can only change
// under a migration, and joka does not migrate and seed in the same command —
// until `joka apply` does, and then the cache has to be built after the
// migrations rather than before.
type keyCache struct {
	db     DBAdapter
	tables map[string][][]string
}

func newKeyCache(db DBAdapter) *keyCache {
	return &keyCache{db: db, tables: make(map[string][][]string)}
}

// of returns the table's unique indexes, reading the catalog the first time.
//
// A table with none caches the empty answer too, so a set full of entities joka
// cannot adopt does not re-ask for each one.
func (c *keyCache) of(ctx context.Context, table string) ([][]string, error) {
	if keys, known := c.tables[table]; known {
		return keys, nil
	}

	keys, err := c.db.UniqueKeys(ctx, table)
	if err != nil {
		return nil, err
	}

	c.tables[table] = keys
	return keys, nil
}

// literalKey narrows an entity's declaration to the named columns, and reports
// whether every one of them is declared as a literal.
func literalKey(e domain.Entity, columns []string) (map[string]any, bool) {
	key := make(map[string]any, len(columns))

	for _, name := range columns {
		value, declared := e.Columns[name]
		if !declared {
			return nil, false
		}
		if s, isString := value.(string); isString && isTemplateString(s) {
			return nil, false
		}
		key[name] = value
	}

	return key, len(key) > 0
}

// liveBaseline reads the declared columns of the row being adopted and hashes
// them, so the entity starts with a record of what was there when joka claimed
// it.
//
// A row that cannot be read after being found is not a case worth tolerating:
// the unique key just matched it, so a failure here is a real error rather than
// a missing row.
func liveBaseline(ctx context.Context, db DBAdapter, e domain.Entity, pk int64) (map[string]string, error) {
	columns := make([]string, 0, len(e.Columns))
	for name := range e.Columns {
		columns = append(columns, name)
	}
	if len(columns) == 0 {
		return nil, nil
	}
	sort.Strings(columns)

	live, err := db.GetRow(ctx, e.Table, columns, e.PKColumn, pk)
	if err != nil {
		return nil, fmt.Errorf("reading %s %s=%d to record what it holds: %w",
			e.Table, e.PKColumn, pk, err)
	}

	return BaselineOf(live), nil
}
