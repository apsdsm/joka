package status

import (
	"context"
	"database/sql"
	"os"

	"github.com/apsdsm/joka/cmd/shared"
	jokastatus "github.com/apsdsm/joka/internal/status"
)

// RunStatusCommand handles "joka status": a read-only inventory of what joka
// has done to this database.
//
// It carries no `joka:mutates` annotation, which is what keeps it out of the
// upgrade gate, the wrong-database refusal and the joka_meta stamp. A status
// that stamped the database it was describing would change the thing it reports
// on, and a status refused for describing the wrong database would be refusing
// to answer the question it exists for.
//
// Exit code is always 0 when the report could be built. It is not a gate —
// `migrate verify` is that for schema, and `entity sync` for seeds.
type RunStatusCommand struct {
	DB            *sql.DB
	Profile       string
	MigrationsDir string
	EntitiesDirs  []string
	StateFile     string
	OutputFormat  string
}

func (r RunStatusCommand) Execute(ctx context.Context) error {
	report, err := jokastatus.Build(ctx, jokastatus.Inputs{
		DB:            r.DB,
		Profile:       r.Profile,
		MigrationsDir: r.MigrationsDir,
		EntitiesDirs:  r.EntitiesDirs,
		StateFile:     r.StateFile,
	})
	if err != nil {
		if r.OutputFormat == shared.OutputJSON {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	if r.OutputFormat == shared.OutputJSON {
		shared.PrintJSON(struct {
			Status string `json:"status"`
			*jokastatus.Report
		}{Status: "ok", Report: report})
		return nil
	}

	jokastatus.Render(os.Stdout, report)
	return nil
}
