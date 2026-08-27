package status

import (
	"context"
	"os"

	templatedomain "github.com/apsdsm/joka/internal/domains/template/domain"
	templateinfra "github.com/apsdsm/joka/internal/domains/template/infra"
)

// buildTemplates compares the rows each template table's record files declare
// against the rows the table actually holds.
//
// Nothing reports this today: `data sync` truncates and reinserts without
// saying what was there before, and there is no `data status`. The comparison
// is only exact for the `truncate` strategy — an `update` table can hold rows
// no file declares, which is why the count is reported rather than judged.
func buildTemplates(ctx context.Context, in Inputs) (Templates, error) {
	out := Templates{Dir: in.TemplatesDir}

	if len(in.Tables) == 0 {
		out.Skipped = "no tables declared in .jokarc.yaml"
		return out, nil
	}

	if info, err := os.Stat(in.TemplatesDir); err != nil || !info.IsDir() {
		out.Skipped = "templates directory not found: " + in.TemplatesDir
		return out, nil
	}

	for _, tc := range in.Tables {
		table := TemplateTable{
			Name:     tc.Name,
			Strategy: string(tc.Strategy),
			Live:     -1,
		}
		if table.Strategy == "" {
			table.Strategy = string(templatedomain.StrategyUpdate)
		}

		// GetTables is all-or-nothing across the tables it is given, so ask it
		// for one table at a time: a single missing directory then reports
		// against that table instead of blanking the whole section.
		found, err := templateinfra.GetTables(in.TemplatesDir, []templateinfra.TableConfig{tc})
		if err != nil {
			table.LoadError = err.Error()
		} else if len(found) == 1 {
			table.Files = len(found[0].Records)
			for _, record := range found[0].Records {
				rows, err := templateinfra.LoadRecord(record)
				if err != nil {
					table.LoadError = err.Error()
					break
				}
				table.Declared += len(rows)
			}
		}

		exists, err := in.Probe.TableExists(ctx, tc.Name)
		if err != nil {
			return out, err
		}
		if exists {
			count, err := in.Probe.CountRows(ctx, tc.Name)
			if err != nil {
				return out, err
			}
			table.Live = count
		} else {
			table.TableMissing = true
		}

		out.Tables = append(out.Tables, table)
	}

	return out, nil
}
