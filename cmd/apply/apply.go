// Package apply brings a database up to date with both halves of what a joka
// root declares: its migrations and its seeds, under one lock and one
// confirmation.
//
// It exists because `joka migrate up && joka entity sync` commits the schema
// change and only then discovers the seeds are wrong, and because a remote
// environment had to be driven by a wrapper script that ran three commands in
// order with a tunnel somebody had opened by hand.
package apply

import (
	"context"
	"database/sql"
	"fmt"
	"io"

	"github.com/apsdsm/joka/cmd/entity"
	"github.com/apsdsm/joka/cmd/migration"
	"github.com/apsdsm/joka/cmd/shared"
	entityapp "github.com/apsdsm/joka/internal/domains/entity/app"
	entitydomain "github.com/apsdsm/joka/internal/domains/entity/domain"
	entityinfra "github.com/apsdsm/joka/internal/domains/entity/infra"
	lockinfra "github.com/apsdsm/joka/internal/domains/lock/infra"
	migrationapp "github.com/apsdsm/joka/internal/domains/migration/app"
	migrationdomain "github.com/apsdsm/joka/internal/domains/migration/domain"
	migrationinfra "github.com/apsdsm/joka/internal/domains/migration/infra"
	"github.com/fatih/color"
)

// RunApplyCommand handles `joka apply`.
type RunApplyCommand struct {
	DB            *sql.DB
	Secrets       entityapp.SecretResolver
	MigrationsDir string
	EntitiesDirs  []string
	AutoConfirm   bool
	OutputFormat  string
	StateFile     string
	Profile       string
	JokaVersion   string
	// DryRun prints the plan and applies nothing. Read-only overall: the
	// speculative pass rolls back, and no lock is taken.
	DryRun bool
	// AllowDelete permits deleting rows no file declares. An interactive run
	// implies it, because the plan named them and somebody said yes.
	AllowDelete bool
	OnConflict  entityapp.ConflictPolicy
}

// Execute plans both halves, confirms once, and applies both.
func (r RunApplyCommand) Execute(ctx context.Context) error {
	jsonOut := r.OutputFormat == shared.OutputJSON

	// Refused up front rather than by the sync at the end. The prompt would
	// land after the combined plan was already approved, giving one command two
	// interaction points and undercutting the one plan it exists to present.
	if r.OnConflict == entityapp.ConflictAsk {
		return fmt.Errorf(
			"--on-conflict=ask is not available under apply: it would ask after the plan was approved.\n" +
				"  Resolve them with 'joka entity sync --on-conflict=ask' first, then apply")
	}

	// One lock for the whole run, as `reset` does. The plan is file-driven and
	// the lock is held across both passes, so it cannot go stale in between.
	if !r.DryRun {
		lock := lockinfra.NewPostgresLockAdapter(r.DB)
		if err := lock.Acquire(ctx, "apply"); err != nil {
			return err
		}
		defer lock.Release(ctx) //nolint:errcheck
	}

	// The migrations table has to exist before the chain can be read, and
	// creating it is idempotent — so apply does it rather than making the
	// caller run `joka init` first, which is the whole point of one command.
	if err := (migration.RunInitCommand{DB: r.DB, OutputFormat: "text", Output: discard(jsonOut)}).Execute(ctx); err != nil {
		return err
	}

	pending, err := pendingMigrations(ctx, r.DB, r.MigrationsDir)
	if err != nil {
		return err
	}

	plan, err := r.planEntities(ctx, pending)
	if err != nil {
		return err
	}

	if jsonOut {
		return r.reportJSON(ctx, pending, plan)
	}

	printPlan(pending, plan)

	if r.DryRun {
		color.Yellow("\nDry run — nothing applied.")
		if len(pending) > 0 || plan.HasChanges() {
			return shared.ErrPendingReported
		}
		return nil
	}

	if len(pending) == 0 && !plan.HasChanges() {
		color.Green("\nAlready up to date.")
		return nil
	}

	allowDelete := r.AllowDelete
	if !r.AutoConfirm {
		fmt.Println()
		if !shared.Confirm("Apply? (only 'yes' will apply): ") {
			color.Yellow("Apply cancelled. Nothing was changed.")
			return shared.ErrCancelled
		}
		// The plan named every row it would delete, first, and somebody read
		// it and agreed. --allow-delete exists for the run nobody watched.
		allowDelete = true
	}

	return r.run(ctx, allowDelete)
}

// planEntities plans the seeds against the schema the migrations will leave.
//
// When migrations are pending it applies them in a transaction, plans through
// the same handle, and rolls back — because a migration that adds a column
// changes what an entity plan means, and planning against the pre-migration
// schema would describe a run that cannot happen. PostgreSQL has transactional
// DDL, so the rollback costs a forward pass and discards the rest.
//
// With nothing pending, which is most runs, it plans against the live schema
// and the two-pass cost never arrives.
func (r RunApplyCommand) planEntities(ctx context.Context, pending []migrationdomain.Migration) (*entityapp.SyncPlan, error) {
	if len(pending) == 0 {
		// No migrations to speculate against, but the seed plan is still a
		// round trip or three per entity, which is its own wait on a remote
		// database.
		say := shared.Progress(r.OutputFormat == shared.OutputJSON)
		say.Start("planning the seeds")
		defer say.Stop()

		if err := entityinfra.NewPostgresStateBackend(r.DB).EnsureStateTable(ctx); err != nil {
			return nil, err
		}

		state, err := entityinfra.NewPostgresStateBackend(r.DB).Load(ctx)
		if err != nil {
			return nil, err
		}

		return r.plan(ctx, entityinfra.NewPostgresDBAdapter(r.DB), state)
	}

	// The state table is created outside the speculation. It is joka's own
	// bookkeeping rather than anything the plan describes, and creating it
	// inside a transaction that is about to roll back would leave the real
	// pass to create it again.
	if err := entityinfra.NewPostgresStateBackend(r.DB).EnsureStateTable(ctx); err != nil {
		return nil, err
	}

	// Said out loud, because this is minutes of work on a remote database and
	// silence is indistinguishable from a hang - which is how it was first
	// reported. 29 migrations and 160 entities over a tunnel to another region
	// is exactly the shape that looks wedged.
	say := shared.Progress(r.OutputFormat == shared.OutputJSON)
	say.Start(fmt.Sprintf("planning against the schema %s will leave",
		shared.Count(len(pending), "pending migration", "pending migrations")))
	defer say.Stop()

	tx, err := r.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("starting the speculative transaction: %w", err)
	}
	// Always: this pass exists to be discarded, and the only path that should
	// commit anything is the real one below.
	defer tx.Rollback() //nolint:errcheck

	if _, err := tx.ExecContext(ctx, "SET LOCAL lock_timeout = '15s'"); err != nil {
		return nil, fmt.Errorf("setting lock_timeout: %w", err)
	}

	migrationTx := migrationinfra.NewPostgresTxDBAdapter(tx, r.DB)
	for i, m := range pending {
		say.Update(fmt.Sprintf("planning against migration %s (%d of %d)", m.MigrationIndex, i+1, len(pending)))

		if err := (migrationapp.ApplyAction{DB: migrationTx, Migration: m, SkipSnapshot: true}).Execute(ctx); err != nil {
			return nil, fmt.Errorf("applying %s (speculatively): %w", m.MigrationIndex, err)
		}
	}

	say.Update("planning the seeds against the migrated schema")

	// Through the same handle, or the plan sees the pre-migration schema and
	// the whole exercise is pointless.
	state, err := entityinfra.NewPostgresTxStateBackend(tx, r.DB).Load(ctx)
	if err != nil {
		return nil, err
	}

	return r.plan(ctx, entityinfra.NewPostgresTxDBAdapter(tx, r.DB), state)
}

// plan builds the entity half against whichever handle it is given.
func (r RunApplyCommand) plan(ctx context.Context, db entityapp.DBAdapter, state *entitydomain.State) (*entityapp.SyncPlan, error) {
	set, err := entity.LoadSet(r.EntitiesDirs, state)
	if err != nil {
		return nil, err
	}

	return entityapp.PlanSyncAction{
		DB:           db,
		State:        state,
		Declared:     set.Files,
		Dirty:        set.Dirty,
		OverriddenBy: set.OverriddenBy,
	}.Execute(ctx)
}

// run applies both halves for real, each in its own transaction.
func (r RunApplyCommand) run(ctx context.Context, allowDelete bool) error {
	color.Cyan("\n[1/2] Applying migrations...")
	if err := (migration.RunMigrateUpCommand{
		DB:            r.DB,
		MigrationsDir: r.MigrationsDir,
		AutoConfirm:   true,
		OutputFormat:  "text",
		SkipLock:      true,
	}).Execute(ctx); err != nil {
		return err
	}

	color.Cyan("\n[2/2] Syncing entities...")

	return entity.RunEntitySyncCommand{
		DB:           r.DB,
		Secrets:      r.Secrets,
		EntitiesDirs: r.EntitiesDirs,
		AutoConfirm:  true,
		OutputFormat: "text",
		StateFile:    r.StateFile,
		Profile:      r.Profile,
		JokaVersion:  r.JokaVersion,
		SkipLock:     true,
		AllowDelete:  allowDelete,
		OnConflict:   r.OnConflict,
	}.Execute(ctx)
}

// pendingMigrations returns the migrations the chain says are unapplied.
func pendingMigrations(ctx context.Context, db *sql.DB, dir string) ([]migrationdomain.Migration, error) {
	chain, err := migrationapp.GetMigrationChainAction{
		DB:            migrationinfra.NewPostgresDBAdapter(db),
		MigrationsDir: dir,
	}.Execute(ctx)
	if err != nil {
		return nil, err
	}

	var pending []migrationdomain.Migration
	for _, m := range chain {
		if m.Status == migrationdomain.StatusPending {
			pending = append(pending, m)
		}
	}

	return pending, nil
}

// printPlan shows both halves, migrations first, because that is the order
// they happen in and the schema is what the seeds are planned against.
func printPlan(pending []migrationdomain.Migration, plan *entityapp.SyncPlan) {
	fmt.Println()
	color.Set(color.Bold)
	fmt.Println("Migrations to apply:")
	color.Unset()

	if len(pending) == 0 {
		fmt.Println("  (none)")
	}
	for _, m := range pending {
		color.Cyan("  %s  %s", m.MigrationIndex, m.FileName)
	}

	// The entity half through the same renderer `entity sync` uses, so the
	// plan a reader confirms here is the plan they would have read there.
	entity.PrintPlan(plan)
}

// reportJSON emits one document describing both halves.
func (r RunApplyCommand) reportJSON(ctx context.Context, pending []migrationdomain.Migration, plan *entityapp.SyncPlan) error {
	indexes := make([]string, 0, len(pending))
	for _, m := range pending {
		indexes = append(indexes, m.MigrationIndex)
	}

	out := map[string]any{
		"status":             "ok",
		"migrations_pending": indexes,
		"plan":               entity.PlanJSON(plan),
	}
	if r.DryRun {
		out["dry_run"] = true
	}

	if r.DryRun {
		shared.PrintJSON(out)
		if len(pending) > 0 || plan.HasChanges() {
			return shared.ErrPendingReported
		}
		return nil
	}

	// --output json implies no prompt, the same as --auto, so there is nothing
	// to confirm and the run proceeds. Deleting still needs --allow-delete:
	// nobody read the plan.
	if len(pending) == 0 && !plan.HasChanges() {
		shared.PrintJSON(out)
		return nil
	}

	return r.run(ctx, r.AllowDelete)
}

// discard gives the init step a writer so its two lines do not land in the
// middle of a plan. It has nothing to say that apply does not say better.
func discard(bool) io.Writer { return io.Discard }
