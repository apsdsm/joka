package entity

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/apsdsm/joka/cmd/shared"
	"github.com/apsdsm/joka/internal/domains/entity/app"
	"github.com/apsdsm/joka/internal/domains/entity/domain"
	"github.com/apsdsm/joka/internal/domains/entity/infra"
	lockinfra "github.com/apsdsm/joka/internal/domains/lock/infra"
	"github.com/fatih/color"
)

// RunEntityForgetCommand handles "entity forget". It removes joka's tracking
// for one or more entity files without touching the rows that tracking points
// at — the opposite of reimport, which replaces the rows and keeps the
// tracking.
type RunEntityForgetCommand struct {
	DB          *sql.DB
	EntitiesDir string
	// FilePath is the relative path to forget. Empty when Orphans is set.
	FilePath string
	// Orphans forgets every tracked file that is no longer on disk.
	Orphans bool
	// Force allows forgetting rows that are still in the database.
	Force        bool
	AutoConfirm  bool
	OutputFormat string
}

func (r RunEntityForgetCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON

	fail := func(err error) error {
		if jsonOut {
			return shared.PrintErrorJSON(err)
		}
		return err
	}

	lockAdapter := lockinfra.NewPostgresLockAdapter(r.DB)
	if err := lockAdapter.Acquire(ctx, "entity forget"); err != nil {
		return fail(err)
	}
	defer lockAdapter.Release(ctx) //nolint:errcheck

	dbAdapter := infra.NewPostgresDBAdapter(r.DB)

	if err := dbAdapter.EnsureTables(ctx); err != nil {
		return fail(err)
	}

	// One read of the state for the whole run, so the targets, every plan and
	// the refusal all describe the same document.
	state, err := infra.NewPostgresStateBackend(r.DB).Load(ctx)
	if err != nil {
		return fail(err)
	}

	targets, err := r.resolveTargets(state)
	if err != nil {
		return fail(err)
	}

	if len(targets) == 0 {
		if jsonOut {
			shared.PrintJSON(map[string]any{"status": "ok", "forgotten": []any{}})
			return nil
		}
		color.Green("No tracked files to forget.")
		return nil
	}

	// Plan every target before touching anything, so the preview covers the
	// whole run and a live-row refusal stops it before the first delete.
	plans := make([]*app.ForgetPlan, 0, len(targets))
	live := 0

	for _, target := range targets {
		plan, err := app.ForgetEntityAction{DB: dbAdapter, State: state, FilePath: target}.Plan(ctx)
		if err != nil {
			return fail(err)
		}
		plans = append(plans, plan)
		live += plan.Live
	}

	if live > 0 && !r.Force {
		if !jsonOut {
			printPlans(plans)
		}
		return fail(fmt.Errorf("%w: %d of %d; use --force to forget them anyway, or 'entity reimport' to replace them",
			domain.ErrRowsStillLive, live, totalRows(plans)))
	}

	if !jsonOut {
		printPlans(plans)

		if !r.AutoConfirm {
			if !shared.Confirm("Forget this tracking? Database rows are not touched (only 'yes' will confirm): ") {
				color.Yellow("Entity forget cancelled.")
				return nil
			}
		}
	}

	forgotten := make([]*app.ForgetPlan, 0, len(plans))

	for _, plan := range plans {
		applied, err := app.ForgetEntityAction{
			DB:       dbAdapter,
			State:    state,
			FilePath: plan.FilePath,
			Force:    r.Force,
		}.Execute(ctx)
		if err != nil {
			return fail(err)
		}
		forgotten = append(forgotten, applied)
	}

	if jsonOut {
		shared.PrintJSON(map[string]any{"status": "ok", "forgotten": forgotten})
		return nil
	}

	rows := 0
	for _, plan := range forgotten {
		rows += len(plan.Rows)
	}

	fmt.Println()
	color.Green("Forgotten. %s removed from tracking across %s.",
		pluralRows(rows), pluralFiles(len(forgotten)))
	if live > 0 {
		color.Yellow("%s left in the database untracked. The next entity sync will insert a second copy.",
			pluralRows(live))
	}
	fmt.Println()

	return nil
}

// resolveTargets returns the file paths to forget: the single argument, or
// every orphan when --orphans is set.
func (r RunEntityForgetCommand) resolveTargets(state *domain.State) ([]string, error) {
	if !r.Orphans {
		return []string{r.FilePath}, nil
	}

	// Orphans come from the same comparison `entity status` reports, so the
	// two can never disagree about which files are orphaned.
	relPaths, err := infra.DiscoverEntityFiles(r.EntitiesDir)
	if err != nil {
		return nil, err
	}

	results, err := app.EntityStatusAction{
		State:       state,
		EntitiesDir: r.EntitiesDir,
		Files:       relPaths,
	}.Execute()
	if err != nil {
		return nil, err
	}

	var targets []string
	for _, info := range results {
		if info.Status == domain.StatusOrphaned {
			targets = append(targets, info.Path)
		}
	}

	return targets, nil
}

// printPlans shows what would be forgotten, one block per file.
func printPlans(plans []*app.ForgetPlan) {
	fmt.Println()
	color.Set(color.Bold)
	fmt.Println("Entity forget:")
	color.Unset()

	for _, plan := range plans {
		fmt.Println()
		color.Cyan("  %s", plan.FilePath)

		if len(plan.Rows) == 0 {
			fmt.Println("    no tracked rows (only the joka_entities record)")
			continue
		}

		for _, row := range plan.Rows {
			label := fmt.Sprintf("    %s  %s %d", row.Table, row.PKColumn, row.PKValue)
			if row.RefID != "" {
				label += fmt.Sprintf("  (_id %s)", row.RefID)
			}

			switch {
			case row.TableMissing:
				color.White("%s  — table no longer exists", label)
			case row.Live:
				color.Yellow("%s  — STILL IN THE DATABASE", label)
			default:
				color.White("%s  — already gone from the database", label)
			}
		}
	}

	fmt.Println()
	fmt.Println("  Database rows are not touched. Files on disk are not touched.")
}

func pluralRows(n int) string {
	if n == 1 {
		return "1 row"
	}
	return fmt.Sprintf("%d rows", n)
}

func pluralFiles(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}

// totalRows counts the tracked rows across every planned file.
func totalRows(plans []*app.ForgetPlan) int {
	n := 0
	for _, plan := range plans {
		n += len(plan.Rows)
	}
	return n
}
