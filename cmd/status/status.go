// Package status implements the top-level `joka status` command: one read-only
// report of what the devops folder declares, what joka's tracking tables
// record, and what the database contains.
package status

import (
	"context"
	"database/sql"
	"os"

	"github.com/apsdsm/joka/cmd/shared"
	entityinfra "github.com/apsdsm/joka/internal/domains/entity/infra"
	lockinfra "github.com/apsdsm/joka/internal/domains/lock/infra"
	migrationinfra "github.com/apsdsm/joka/internal/domains/migration/infra"
	templateinfra "github.com/apsdsm/joka/internal/domains/template/infra"
	jokastatus "github.com/apsdsm/joka/internal/status"
)

// RunStatusCommand handles the "status" command. It writes nothing — including
// the joka_* tracking tables that the other commands auto-create, since a
// missing tracking table is one of the things worth reporting.
type RunStatusCommand struct {
	DB            *sql.DB
	Profile       string
	MigrationsDir string
	TemplatesDir  string
	EntitiesDir   string
	Tables        []templateinfra.TableConfig
	// Compact renders the report as a single line instead of the full report.
	Compact      bool
	OutputFormat string
}

func (r RunStatusCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON

	report, err := jokastatus.Build(ctx, jokastatus.Inputs{
		Conn:          r.DB,
		Profile:       r.Profile,
		Driver:        "postgres",
		MigrationsDir: r.MigrationsDir,
		EntitiesDir:   r.EntitiesDir,
		TemplatesDir:  r.TemplatesDir,
		Tables:        r.Tables,
		Migration:     migrationinfra.NewPostgresDBAdapter(r.DB),
		Entity:        entityinfra.NewPostgresDBAdapter(r.DB),
		Lock:          lockinfra.NewPostgresLockAdapter(r.DB),
		Probe:         jokastatus.NewProbe(r.DB),
	})
	if err != nil {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	if jsonOut {
		shared.PrintJSON(struct {
			Status string `json:"status"`
			jokastatus.Report
		}{Status: "ok", Report: report})
		return nil
	}

	if r.Compact {
		jokastatus.RenderCompact(os.Stdout, report)
		return nil
	}

	jokastatus.RenderText(os.Stdout, report)
	return nil
}
