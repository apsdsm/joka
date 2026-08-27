package status

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strings"

	jokadb "github.com/apsdsm/joka/db"
)

// Probe answers the questions status asks of the database directly: whether a
// table exists, how many rows it holds, and which tracked primary keys are
// still present. Read-only.
type Probe interface {
	// TableExists reports whether the named table exists in the current
	// database/schema. Results are expected to be cached by the caller.
	TableExists(ctx context.Context, table string) (bool, error)
	// CountRows returns the number of rows in the table.
	CountRows(ctx context.Context, table string) (int, error)
	// ExistingPKs returns the subset of pks that are still present in the
	// table. Used to tell how many of an entity file's tracked rows survive.
	ExistingPKs(ctx context.Context, table, pkColumn string, pks []int64) (map[int64]struct{}, error)
}

// identRE guards the identifiers status interpolates into SQL. Table and
// column names here come from entity YAML (_is, _pk) and the .jokarc.yaml
// tables list, so they are project-authored rather than user input, but they
// are still not parameterisable — reject anything that is not a plain
// identifier instead of quoting and hoping.
var identRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*$`)

// pkBatchSize caps how many primary keys go into one IN list. Entity files can
// track hundreds of rows and both drivers have a parameter limit.
const pkBatchSize = 500

type dbProbe struct {
	conn *sql.DB
}

// NewProbe returns a Probe backed by the given connection.
func NewProbe(conn *sql.DB) Probe {
	return dbProbe{conn: conn}
}

func (p dbProbe) TableExists(ctx context.Context, table string) (bool, error) {
	if !identRE.MatchString(table) {
		return false, fmt.Errorf("refusing to query table with unexpected name %q", table)
	}
	return jokadb.TableExists(ctx, p.conn, table)
}

func (p dbProbe) CountRows(ctx context.Context, table string) (int, error) {
	quoted, err := p.quote(table)
	if err != nil {
		return 0, err
	}

	var count int
	if err := p.conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoted).Scan(&count); err != nil {
		return 0, fmt.Errorf("counting rows in %s: %w", table, err)
	}
	return count, nil
}

func (p dbProbe) ExistingPKs(ctx context.Context, table, pkColumn string, pks []int64) (map[int64]struct{}, error) {
	found := make(map[int64]struct{}, len(pks))
	if len(pks) == 0 {
		return found, nil
	}

	quotedTable, err := p.quote(table)
	if err != nil {
		return nil, err
	}
	quotedPK, err := p.quote(pkColumn)
	if err != nil {
		return nil, err
	}

	for start := 0; start < len(pks); start += pkBatchSize {
		end := min(start+pkBatchSize, len(pks))
		batch := pks[start:end]

		placeholders := make([]string, len(batch))
		args := make([]any, len(batch))
		for i, pk := range batch {
			placeholders[i] = p.placeholder(i + 1)
			args[i] = pk
		}

		query := fmt.Sprintf("SELECT %s FROM %s WHERE %s IN (%s)",
			quotedPK, quotedTable, quotedPK, strings.Join(placeholders, ", "))

		rows, err := p.conn.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("checking rows in %s: %w", table, err)
		}

		for rows.Next() {
			var pk int64
			if err := rows.Scan(&pk); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scanning primary key from %s: %w", table, err)
			}
			found[pk] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("reading primary keys from %s: %w", table, err)
		}
		rows.Close()
	}

	return found, nil
}

// quote wraps an identifier in double quotes after checking it is a plain
// identifier.
func (p dbProbe) quote(ident string) (string, error) {
	if !identRE.MatchString(ident) {
		return "", fmt.Errorf("refusing to query identifier with unexpected name %q", ident)
	}
	return `"` + ident + `"`, nil
}

// placeholder returns the positional placeholder for argument n (1-based).
func (p dbProbe) placeholder(n int) string {
	return fmt.Sprintf("$%d", n)
}
