package app

import (
	"context"
	"errors"

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
// The row is claimed, not compared: an adopted entity gets a nil baseline, and
// a nil baseline reads as push, so the declaration is written over whatever the
// row holds and the plan shows every column it changes. That is the destructive
// reading, and it is the right one — the seed files are the desired state, and
// a row joka is being told to own should end up saying what they say.
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
	db DBAdapter,
	e domain.Entity,
	file string,
	order int,
) (Adoption, bool, error) {
	keys, err := db.UniqueKeys(ctx, e.Table)
	if err != nil {
		return Adoption{}, false, err
	}

	for _, columns := range keys {
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
				// No baseline. joka did not write this row, so it has no record
				// of applying anything to it, and a nil baseline is exactly
				// that statement — every difference reads as push.
				Columns: nil,
			},
		}, true, nil
	}

	return Adoption{}, false, nil
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
