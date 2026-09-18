package infra

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/domains/entity/domain"
)

// PostgresStateBackend keeps the state document in the database it describes,
// in the joka_entities and joka_entity_rows tables.
//
// The document is stored decomposed across those two tables rather than as one
// value. That is what they already held, and version 1 of the state shape names
// it rather than changing it — moving the document into a single jsonb column
// is a later step with its own tracking version bump.
//
// Save writes only what differs from what is already recorded. Rewriting every
// row would be simpler, but joka_entities.synced_at is a column a human reads,
// and resetting it on files nothing touched would make it lie.
type PostgresStateBackend struct {
	db   DBTX
	conn *sql.DB
}

// NewPostgresStateBackend reads and writes on the raw connection.
func NewPostgresStateBackend(conn *sql.DB) *PostgresStateBackend {
	return &PostgresStateBackend{db: conn, conn: conn}
}

// NewPostgresTxStateBackend reads and writes inside the given transaction, so
// the document commits with the rows it describes. Table existence is checked
// on the raw connection, because the tables are created by DDL that cannot run
// in the transaction.
func NewPostgresTxStateBackend(tx *sql.Tx, conn *sql.DB) *PostgresStateBackend {
	return &PostgresStateBackend{db: tx, conn: conn}
}

// Load reads the state of the database.
//
// A database with no tracking tables loads as an empty state rather than an
// error: it has never been written, which is a fact about it and not a failure
// to read it. This is also what lets a read-only caller use the backend without
// creating anything.
func (b *PostgresStateBackend) Load(ctx context.Context) (*domain.State, error) {
	state := domain.NewState()

	hasFiles, err := jokadb.TableExists(ctx, b.conn, "joka_entities")
	if err != nil {
		return nil, err
	}
	if hasFiles {
		if err := b.loadFiles(ctx, state); err != nil {
			return nil, err
		}
	}

	hasRows, err := jokadb.TableExists(ctx, b.conn, "joka_entity_rows")
	if err != nil {
		return nil, err
	}
	if hasRows {
		if err := b.loadEntities(ctx, state); err != nil {
			return nil, err
		}
	}

	return state, nil
}

func (b *PostgresStateBackend) loadFiles(ctx context.Context, state *domain.State) error {
	rows, err := b.db.QueryContext(ctx,
		`SELECT entity_file, content_hash FROM joka_entities`)
	if err != nil {
		return fmt.Errorf("reading tracked entity files: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var path string
		var hash sql.NullString
		if err := rows.Scan(&path, &hash); err != nil {
			return fmt.Errorf("scanning a tracked entity file: %w", err)
		}
		state.TrackFile(path, hash.String)
	}

	return rows.Err()
}

func (b *PostgresStateBackend) loadEntities(ctx context.Context, state *domain.State) error {
	rows, err := b.db.QueryContext(ctx,
		`SELECT entity_file, table_name, row_pk, pk_column, ref_id, insertion_order
		 FROM joka_entity_rows ORDER BY entity_file, insertion_order`)
	if err != nil {
		return fmt.Errorf("reading tracked entity rows: %w", err)
	}
	defer rows.Close()

	claimed := make(map[string]string)

	for rows.Next() {
		var row domain.TrackedRow
		var refID sql.NullString

		if err := rows.Scan(&row.EntityFile, &row.TableName, &row.RowPK,
			&row.PKColumn, &refID, &row.InsertionOrder); err != nil {
			return fmt.Errorf("scanning a tracked entity row: %w", err)
		}
		row.RefID = refID.String

		// A row written before joka recorded an _id. It cannot be keyed, and
		// dropping it would lose the row it points at, so it is carried where a
		// reader can report it.
		if row.RefID == "" {
			state.Unkeyed = append(state.Unkeyed, row)
			continue
		}

		if first, dup := claimed[row.RefID]; dup {
			return fmt.Errorf("%w: %q is claimed by a row in %s and another in %s; "+
				"drop one file's tracking with 'joka entity forget <file>'",
				domain.ErrStateAmbiguous, row.RefID, first, row.EntityFile)
		}
		claimed[row.RefID] = row.EntityFile

		state.Track(row.RefID, domain.EntityState{
			Table:    row.TableName,
			PKColumn: row.PKColumn,
			PKValue:  row.RowPK,
			File:     row.EntityFile,
			Order:    row.InsertionOrder,
		})
	}

	return rows.Err()
}

// Save writes back what differs from the recorded state.
//
// An _id present in the document and absent from the database is inserted, one
// whose record changed is updated, and one the document dropped has its
// tracking deleted — never the row it points at. Deleting tracking is how
// `entity forget` works, and it is reachable only by removing the entry, not by
// failing to mention it: a caller saves the document it loaded, so an entity it
// never looked at is still in the map.
func (b *PostgresStateBackend) Save(ctx context.Context, state *domain.State) error {
	current, err := b.Load(ctx)
	if err != nil {
		return err
	}

	if err := b.saveFiles(ctx, current, state); err != nil {
		return err
	}

	return b.saveEntities(ctx, current, state)
}

func (b *PostgresStateBackend) saveFiles(ctx context.Context, current, next *domain.State) error {
	for _, path := range sortedFileKeys(next.Files) {
		want := next.Files[path]
		have, tracked := current.Files[path]

		switch {
		case !tracked:
			if _, err := b.db.ExecContext(ctx,
				`INSERT INTO joka_entities (entity_file, content_hash) VALUES ($1, $2)
				 ON CONFLICT (entity_file) DO UPDATE
				 SET content_hash = EXCLUDED.content_hash, synced_at = NOW()`,
				path, want.ContentHash,
			); err != nil {
				return fmt.Errorf("recording %s as synced: %w", path, err)
			}
		case have.ContentHash != want.ContentHash:
			if _, err := b.db.ExecContext(ctx,
				`UPDATE joka_entities SET content_hash = $1, synced_at = NOW() WHERE entity_file = $2`,
				want.ContentHash, path,
			); err != nil {
				return fmt.Errorf("updating the record of %s: %w", path, err)
			}
		}
	}

	for _, path := range sortedFileKeys(current.Files) {
		if _, keep := next.Files[path]; keep {
			continue
		}
		if _, err := b.db.ExecContext(ctx,
			`DELETE FROM joka_entities WHERE entity_file = $1`, path,
		); err != nil {
			return fmt.Errorf("removing the tracking record for %s: %w", path, err)
		}
	}

	return nil
}

func (b *PostgresStateBackend) saveEntities(ctx context.Context, current, next *domain.State) error {
	for _, refID := range sortedEntityKeys(next.Entities) {
		want := next.Entities[refID]
		have, tracked := current.Entities[refID]

		if !tracked {
			if _, err := b.db.ExecContext(ctx,
				`INSERT INTO joka_entity_rows
				 (entity_file, table_name, row_pk, pk_column, ref_id, insertion_order)
				 VALUES ($1, $2, $3, $4, $5, $6)`,
				want.File, want.Table, want.PKValue, want.PKColumn, refID, want.Order,
			); err != nil {
				return fmt.Errorf("tracking %s row %d as %q: %w", want.Table, want.PKValue, refID, err)
			}
			continue
		}

		if have == want {
			continue
		}

		if _, err := b.db.ExecContext(ctx,
			`UPDATE joka_entity_rows
			 SET entity_file = $1, table_name = $2, row_pk = $3, pk_column = $4, insertion_order = $5
			 WHERE ref_id = $6`,
			want.File, want.Table, want.PKValue, want.PKColumn, want.Order, refID,
		); err != nil {
			return fmt.Errorf("re-pointing the tracking for %q: %w", refID, err)
		}
	}

	for _, refID := range sortedEntityKeys(current.Entities) {
		if _, keep := next.Entities[refID]; keep {
			continue
		}
		if _, err := b.db.ExecContext(ctx,
			`DELETE FROM joka_entity_rows WHERE ref_id = $1`, refID,
		); err != nil {
			return fmt.Errorf("removing the tracking for %q: %w", refID, err)
		}
	}

	return nil
}

// sortedFileKeys and sortedEntityKeys make the statements a save issues depend
// only on the data, not on map iteration order, so a failure part-way through
// is reproducible.
func sortedFileKeys(m map[string]domain.FileState) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedEntityKeys(m map[string]domain.EntityState) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
