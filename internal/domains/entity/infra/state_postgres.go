package infra

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/domains/entity/domain"
	"github.com/apsdsm/joka/internal/meta"
)

// StateKey is the joka_state row the entity document lives under.
//
// The table is keyed rather than single-row so migration bookkeeping could move
// into it later without another table. Only this key is written today; see
// proposal_entity_convergence_20260918.md, open question 2.
const StateKey = "entities"

// PostgresStateBackend keeps the state document in the database it describes,
// as one jsonb value in joka_state.
//
// It reads two layouts. Tracking version 3 and later store the document; before
// that the same information was decomposed across joka_entities and
// joka_entity_rows, and a database still on version 2 is read from those. That
// path exists because read-only commands never upgrade, so `joka status` has to
// describe a database no mutating command has touched yet.
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
// on the raw connection, because the table is created by DDL that cannot run in
// the transaction.
func NewPostgresTxStateBackend(tx *sql.Tx, conn *sql.DB) *PostgresStateBackend {
	return &PostgresStateBackend{db: tx, conn: conn}
}

// EnsureStateTable creates the tables a save writes: joka_state for the
// document, and joka_meta for the counter that goes with it.
func (b *PostgresStateBackend) EnsureStateTable(ctx context.Context) error {
	// Save counts the write in joka_meta, in the same transaction, so the table
	// has to be there before a command starts writing.
	if err := meta.EnsureTable(ctx, b.conn); err != nil {
		return err
	}

	exists, err := jokadb.TableExists(ctx, b.conn, "joka_state")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	_, err = b.conn.ExecContext(ctx, `
		CREATE TABLE joka_state (
			key VARCHAR(64) PRIMARY KEY,
			doc JSONB NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`)
	if err != nil {
		return fmt.Errorf("creating joka_state: %w", err)
	}
	return nil
}

// Load reads the state of the database.
//
// A database with nothing to read loads as an empty state rather than an error:
// it has never been written, which is a fact about it and not a failure to read
// it. Nothing is created, which is what lets a read-only caller use the backend.
func (b *PostgresStateBackend) Load(ctx context.Context) (*domain.State, error) {
	hasState, err := jokadb.TableExists(ctx, b.conn, "joka_state")
	if err != nil {
		return nil, err
	}

	if hasState {
		state, found, err := b.loadDocument(ctx)
		if err != nil {
			return nil, err
		}
		if found {
			return state, nil
		}
	}

	return b.loadLegacy(ctx)
}

// Save writes the document back, replacing whatever was there, and counts the
// write in joka_meta.
//
// One statement for the document, so a save is atomic on its own and inside the
// caller's transaction it commits with the rows it describes. The decomposed
// layout it replaced needed a diff against the recorded state to avoid
// rewriting rows nothing had touched; a document has nothing to diff.
//
// The version counter moves in the same transaction, because a count that could
// commit without the write it counts would be worse than no counter at all. The
// database's identity is assigned here too if it does not have one — it is what
// a state file on disk is checked against.
func (b *PostgresStateBackend) Save(ctx context.Context, state *domain.State) error {
	if state.Version == 0 {
		state.Version = domain.StateVersion
	}

	doc, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encoding the state document: %w", err)
	}

	_, err = b.db.ExecContext(ctx,
		`INSERT INTO joka_state (key, doc) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET doc = EXCLUDED.doc, updated_at = NOW()`,
		StateKey, doc,
	)
	if err != nil {
		return fmt.Errorf("saving the state document: %w", err)
	}

	proposed, err := NewIdentity()
	if err != nil {
		return err
	}
	if _, err := meta.StampStateWrite(ctx, b.db, proposed); err != nil {
		return err
	}

	return nil
}

// loadDocument reads joka_state. found is false when the table is there but
// holds no document yet, which is how a database upgraded before anything was
// synced reads.
func (b *PostgresStateBackend) loadDocument(ctx context.Context) (*domain.State, bool, error) {
	var doc []byte

	err := b.db.QueryRowContext(ctx,
		`SELECT doc FROM joka_state WHERE key = $1`, StateKey).Scan(&doc)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("reading the state document: %w", err)
	}

	state := domain.NewState()
	if err := json.Unmarshal(doc, state); err != nil {
		return nil, false, fmt.Errorf("decoding the state document: %w", err)
	}

	// An empty object decodes to nil maps, and every caller expects to be able
	// to write into them.
	if state.Files == nil {
		state.Files = make(map[string]domain.FileState)
	}
	if state.Entities == nil {
		state.Entities = make(map[string]domain.EntityState)
	}

	return state, true, nil
}

// loadLegacy reads the decomposed layout that tracking versions 1 and 2 used.
func (b *PostgresStateBackend) loadLegacy(ctx context.Context) (*domain.State, error) {
	state := domain.NewState()

	hasFiles, err := jokadb.TableExists(ctx, b.conn, "joka_entities")
	if err != nil {
		return nil, err
	}
	if hasFiles {
		if err := b.loadLegacyFiles(ctx, state); err != nil {
			return nil, err
		}
	}

	hasRows, err := jokadb.TableExists(ctx, b.conn, "joka_entity_rows")
	if err != nil {
		return nil, err
	}
	if hasRows {
		if err := b.loadLegacyEntities(ctx, state); err != nil {
			return nil, err
		}
	}

	return state, nil
}

func (b *PostgresStateBackend) loadLegacyFiles(ctx context.Context, state *domain.State) error {
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

func (b *PostgresStateBackend) loadLegacyEntities(ctx context.Context, state *domain.State) error {
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
		// reader can report it. The version 3 upgrade refuses while any remain.
		if row.RefID == "" {
			state.Unkeyed = append(state.Unkeyed, row)
			continue
		}

		if first, dup := claimed[row.RefID]; dup {
			return fmt.Errorf("%w: %q is claimed by a row in %s and another in %s; drop the losing "+
				"claim directly, e.g. DELETE FROM joka_entity_rows WHERE entity_file = '%s'",
				domain.ErrStateAmbiguous, row.RefID, first, row.EntityFile, row.EntityFile)
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
