package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/apsdsm/joka/cmd/dbtools"
	"github.com/apsdsm/joka/cmd/entity"
	"github.com/apsdsm/joka/cmd/lock"
	"github.com/apsdsm/joka/cmd/migration"
	"github.com/apsdsm/joka/cmd/shared"
	"github.com/apsdsm/joka/cmd/status"
	"github.com/apsdsm/joka/config"
	jokadb "github.com/apsdsm/joka/db"
	"github.com/apsdsm/joka/internal/connection"
	entityapp "github.com/apsdsm/joka/internal/domains/entity/app"
	"github.com/apsdsm/joka/internal/domains/entity/domain"
	entityinfra "github.com/apsdsm/joka/internal/domains/entity/infra"
	"github.com/apsdsm/joka/internal/meta"
	"github.com/apsdsm/joka/internal/secrets"
	"github.com/apsdsm/joka/internal/upgrade"
	"github.com/fatih/color"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
)

const version = "0.14.0"

// annotationMutates marks a command that writes to the database, so the root
// command knows whether to stamp joka_meta. Read-only commands must not.
const annotationMutates = "joka:mutates"

// annotationWipes marks a command that destroys the tracking rather than
// reading it. Such a command still writes, so it stamps — but it must not be
// gated on an upgrade of bookkeeping it is about to delete.
//
// Without this, a database whose upgrade is blocked has no way out: every
// command that could clear the blockage is itself blocked by it.
const annotationWipes = "joka:wipes"

// annotationNeeds names the directories a command reads, comma separated:
// "migrations", "entities", or both. The root checks they are there before it
// opens a connection — see shared.RequireDir for why that ordering matters.
const annotationNeeds = "joka:needs"

// mutates is the annotation map for a command that writes.
var mutates = map[string]string{annotationMutates: "true"}

// needs returns base with a directory list added, so one command can declare
// both what it writes and what it reads.
func needs(list string, base map[string]string) map[string]string {
	merged := map[string]string{annotationNeeds: list}
	for k, v := range base {
		merged[k] = v
	}
	return merged
}

// wipes is the annotation map for a command that destroys the tracking.
var wipes = map[string]string{annotationMutates: "true", annotationWipes: "true"}

func main() {
	var (
		envFile       string
		profile       string
		migrationsDir string
		entitiesDir   string
		stateFile     string
		rootName      string
		adoptRoot     bool
		autoConfirm   bool
		outputFormat  string
		dbConn        *sql.DB
		dbDSN         string
		cfg           *config.Config
	)

	root := &cobra.Command{
		Use: "joka",
		// A failed command prints its error once, and prints no flags. Cobra's
		// own handling did both wrong: it dumped a screen of flags after every
		// error, burying the message that named what to do next, and it printed
		// the error itself on top of main's own handling below.
		SilenceUsage:  true,
		SilenceErrors: true,
		Short:         "Database migration management tool",
		PersistentPreRunE: func(c *cobra.Command, args []string) error {
			if err := shared.ValidateOutputFlag(outputFormat); err != nil {
				return err
			}

			var err error
			cfg, err = config.Load(profile)
			if err != nil {
				return err
			}

			if !c.Flags().Changed("migrations") && cfg.Migrations != "" {
				migrationsDir = cfg.Migrations
			}
			if !c.Flags().Changed("entities") && cfg.Entities != "" {
				entitiesDir = cfg.Entities
			}
			if !c.Flags().Changed("statefile") && cfg.StateFile != "" {
				stateFile = cfg.StateFile
			}
			if !c.Flags().Changed("root") && cfg.Root != "" {
				rootName = cfg.Root
			}

			if c.Name() == "version" {
				return nil
			}

			// Refuse before a connection is opened when a directory the command
			// reads is not there. Opening one first meant a command run from
			// the wrong directory upgraded the tracking and created joka_meta,
			// joka_state and joka_lock in a database it was never meant to
			// reach, and only then noticed it had nothing to read.
			if err := requireDirs(c, migrationsDir, entitiesDir, cfg); err != nil {
				return err
			}

			// Load any --env dotenv first so the "env" connection source (and
			// anything else relying on process env) sees it.
			if err := loadEnv(envFile); err != nil {
				return err
			}

			// `migrate new` writes a file and never opens a connection, so it
			// runs without a reachable database.
			if c.Name() == "new" {
				return nil
			}

			// Resolve the DSN from the connection config (env by default, or a
			// secret source declared in .jokarc.yaml / the selected profile).
			dsn, err := connection.Resolve(c.Context(), cfg.Connection, nil)
			if err != nil {
				return err
			}
			// Kept for `migrate consolidate`, which hands it to pg_dump.
			dbDSN = dsn

			dbConn, err = jokadb.Open(dsn)
			if err != nil {
				return fmt.Errorf("error connecting to database: %w", err)
			}

			// Refuse a database whose bookkeeping a newer joka has already
			// moved on: this build would read the new shape as the old one.
			if err := meta.Check(c.Context(), dbConn); err != nil {
				return err
			}

			// Bring the bookkeeping up to date and record what wrote here, but
			// only for commands that write. A
			// read-only command must leave a bare database bare — that is what
			// makes a missing tracking table reportable.
			// The root guard applies to drop and reset as well, which is where
			// it parts company with the two gates below.
			//
			// Those two skip a wiping command for reasons that do not carry
			// over. The upgrade gate skips because a blocked upgrade would
			// otherwise have no command left that could clear it. The
			// wrong-database gate skips because drop takes joka_meta with it,
			// so a wiped database has no identity while the state file beside
			// the checkout still has one — which made drop followed by init
			// fail with nothing left to run.
			//
			// Ownership has neither problem. The claim lives in a committed
			// configuration rather than in a file a wipe destroys, and a root
			// that genuinely means to take a database over says so with
			// --adopt-root. Dropping the wrong environment's database is the
			// worst thing joka can do, so it is the last command that should
			// skip the check that names the owner.
			if c.Annotations[annotationMutates] == "true" {
				if err := refuseWrongRoot(c, dbConn, rootName, adoptRoot); err != nil {
					return err
				}
			}

			if c.Annotations[annotationMutates] == "true" && c.Annotations[annotationWipes] != "true" {
				if err := refuseWrongDatabase(c, stateFile, profile, dbConn); err != nil {
					return err
				}

				applied, err := upgrade.Run(c.Context(), dbConn, version)
				if err != nil {
					return err
				}
				for _, step := range applied {
					if outputFormat != shared.OutputJSON {
						color.Cyan("Upgraded tracking to version %d: %s", step.To, step.Describe)
					}
				}
			}

			return nil
		},
		PersistentPostRunE: func(c *cobra.Command, args []string) error {
			if dbConn == nil {
				return nil
			}
			defer dbConn.Close()

			// A wiping command skips the upgrade gate on the way in, and the
			// stamp lives inside it, so `drop` and `reset` would otherwise leave
			// a database this build just wrote with no marker on it. The next
			// mutating command would read that as pre-marker and announce an
			// upgrade of bookkeeping it had itself written a second ago.
			//
			// Stamping here rather than there is deliberate: what the database
			// holds afterwards is what this build writes, and that is only true
			// once the command has finished. Cobra runs PersistentPostRunE only
			// on success, so a failed reset leaves the marker alone.
			if c.Annotations[annotationWipes] == "true" {
				if err := meta.Stamp(c.Context(), dbConn, version); err != nil {
					return err
				}
			}

			// The claim is made on the way out, for the same reason the wipe
			// stamp is: a root owns a database once it has successfully written
			// to it, and cobra runs PersistentPostRunE only on success. A run
			// that failed has not taken ownership of anything.
			//
			// This covers drop and reset too. They take joka_meta with them, so
			// without a re-claim here a wipe would quietly release a database
			// its own root had claimed.
			if c.Annotations[annotationMutates] == "true" {
				claim := meta.ClaimRoot
				if adoptRoot {
					claim = meta.AdoptRoot
				}
				if err := claim(c.Context(), dbConn, rootName); err != nil {
					return err
				}
			}

			return nil
		},
	}

	root.PersistentFlags().StringVarP(&envFile, "env", "e", ".env", "Path to the environment file")
	root.PersistentFlags().StringVarP(&profile, "profile", "p", "", "Config profile to use (from .jokarc.yaml profiles)")
	root.PersistentFlags().StringVarP(&migrationsDir, "migrations", "m", "devops/migrations", "Path to the migrations directory")
	root.PersistentFlags().StringVar(&entitiesDir, "entities", "devops/entities", "Path to the entities directory")
	root.PersistentFlags().StringVar(&stateFile, "statefile", "", "Path to the state file (default: joka[.<profile>].state.json beside the working directory)")
	root.PersistentFlags().StringVar(&rootName, "root", "", "Name of this joka root (default: the 'root:' key in .jokarc.yaml)")
	root.PersistentFlags().BoolVar(&adoptRoot, "adopt-root", false, "Move this database's root claim to this root")
	root.PersistentFlags().BoolVarP(&autoConfirm, "auto", "a", false, "Automatically confirm prompts")
	root.PersistentFlags().StringVarP(&outputFormat, "output", "o", "text", "Output format: text or json")

	// No annotation. Status is read-only, so it stays out of the upgrade gate,
	// the wrong-database refusal and the joka_meta stamp — a status that stamped
	// the database would change what it reports on, and one refused for
	// describing the wrong database would refuse the question it exists for.
	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Report what joka has done to this database",
		Long: `Report what joka has done to this database.

An inventory, not a diff: status answers "what is", where a plan answers "what
would change". Read-only, creates nothing, and always exits 0 when the report
could be built — 'joka migrate verify' is the drift gate for schema and
'joka entity sync' for seeds.`,
		RunE: func(c *cobra.Command, _ []string) error {
			return status.RunStatusCommand{
				DB:            dbConn,
				Profile:       profile,
				MigrationsDir: migrationsDir,
				EntitiesDir:   entitiesDir,
				StateFile:     stateFile,
				OutputFormat:  outputFormat,
			}.Execute(c.Context())
		},
	}

	initCmd := &cobra.Command{
		Use:         "init",
		Short:       "Initialize the migrations table",
		Annotations: mutates,
		RunE: func(c *cobra.Command, _ []string) error {
			return migration.RunInitCommand{DB: dbConn, OutputFormat: outputFormat}.Execute(c.Context())
		},
	}

	migrateNewCmd := &cobra.Command{
		Use:         "new [name]",
		Short:       "Create a new migration file",
		Annotations: needs("migrations", nil),
		Args:        cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return migration.RunMakeCommand{
				MigrationsDir: migrationsDir,
				Name:          args[0],
				OutputFormat:  outputFormat,
			}.Execute(c.Context())
		},
	}

	migrateCmd := &cobra.Command{
		Use:   "migrate",
		Short: "Database migration commands",
	}

	migrateUpCmd := &cobra.Command{
		Use:         "up",
		Short:       "Apply pending migrations",
		Annotations: needs("migrations", mutates),
		RunE: func(c *cobra.Command, _ []string) error {
			return migration.RunMigrateUpCommand{
				DB:            dbConn,
				MigrationsDir: migrationsDir,
				AutoConfirm:   autoConfirm,
				OutputFormat:  outputFormat,
			}.Execute(c.Context())
		},
	}

	migrateStatusCmd := &cobra.Command{
		Use:         "status",
		Short:       "Show migration status",
		Annotations: needs("migrations", nil),
		RunE: func(c *cobra.Command, _ []string) error {
			return migration.RunMigrateStatusCommand{
				DB:            dbConn,
				MigrationsDir: migrationsDir,
				OutputFormat:  outputFormat,
			}.Execute(c.Context())
		},
	}

	unlockCmd := &cobra.Command{
		Use:         "unlock",
		Short:       "Force-release a held lock",
		Annotations: mutates,
		RunE: func(c *cobra.Command, _ []string) error {
			return lock.RunUnlockCommand{DB: dbConn, OutputFormat: outputFormat}.Execute(c.Context())
		},
	}

	migrateConsolidateCmd := &cobra.Command{
		Use:         "consolidate",
		Short:       "Squash applied migrations into one baseline dumped by pg_dump (PostgreSQL only)",
		Annotations: needs("migrations", mutates),
		RunE: func(c *cobra.Command, _ []string) error {
			upTo, _ := c.Flags().GetString("up-to")
			if upTo == "" {
				return fmt.Errorf("--up-to flag is required")
			}
			return migration.RunConsolidateCommand{
				DB:            dbConn,
				DSN:           dbDSN,
				MigrationsDir: migrationsDir,
				UpToIndex:     upTo,
				AutoConfirm:   autoConfirm,
				OutputFormat:  outputFormat,
			}.Execute(c.Context())
		},
	}
	migrateConsolidateCmd.Flags().String("up-to", "", "Migration index to consolidate up to (required; must be the last applied migration)")

	migrateVerifyCmd := &cobra.Command{
		Use:         "verify",
		Short:       "Detect schema drift against the latest snapshot",
		Annotations: needs("migrations", nil),
		RunE: func(c *cobra.Command, _ []string) error {
			return migration.RunVerifyCommand{
				DB:           dbConn,
				OutputFormat: outputFormat,
			}.Execute(c.Context())
		},
	}

	entityCmd := &cobra.Command{
		Use:   "entity",
		Short: "Entity graph management commands",
	}

	entitySyncCmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync entity YAML files to the database",
		Long: `Sync entity YAML files to the database.

The seed files are the desired state. Every declared entity is compared against
the database, whether or not its file changed. A tracked entity has each declared
column compared three ways — against the file, against the database, and against
what joka last wrote there.

An entity joka does not track is looked for by a unique key its declaration
fills in. Found, the row is claimed and the declaration written over it, which is
reported before it happens. Not found, it is inserted.

A column only the file moved is written. A column the database moved is a
conflict: applying the file would discard a change joka did not make.

  --on-conflict=fail   report them, write nothing, exit non-zero (default)
  --on-conflict=file   the file wins; write over the database's values
  --on-conflict=db     the database wins; keep its values and rewrite the seed files
  --on-conflict=ask    show each one and ask, updating the seed files to match
                       the database where you say it is right

The default makes this a drift gate, the same role 'migrate verify' plays for
schema. A column listed under an entity's _once: is not compared at all — joka
seeds it on insert and the database owns it after, which is how a password
survives a re-sync.

Use --decayed when the database's copy of the seeded data has rotted and the
files are the only version worth keeping: every declared column is rewritten and
nothing is reported as a conflict. _once is still honoured.

Use --dry-run to print the plan without applying anything.`,
		Annotations: needs("entities", mutates),
		RunE: func(c *cobra.Command, _ []string) error {
			dryRun, _ := c.Flags().GetBool("dry-run")
			decayed, _ := c.Flags().GetBool("decayed")

			onConflict, _ := c.Flags().GetString("on-conflict")
			policy, err := entityapp.ParseConflictPolicy(onConflict)
			if err != nil {
				return err
			}

			return entity.RunEntitySyncCommand{
				DB:           dbConn,
				Secrets:      secrets.New(cfg.Secrets),
				EntitiesDir:  entitiesDir,
				AutoConfirm:  autoConfirm,
				OutputFormat: outputFormat,
				DryRun:       dryRun,
				Decayed:      decayed,
				OnConflict:   policy,
				Profile:      profile,
				StateFile:    stateFile,
				JokaVersion:  version,
			}.Execute(c.Context())
		},
	}
	entitySyncCmd.Flags().Bool("dry-run", false, "Preview inserts and before/after changes without applying")
	entitySyncCmd.Flags().String("on-conflict", "fail",
		"What to do when the database changed since joka last wrote: fail, file, db or ask")
	entitySyncCmd.Flags().Bool("decayed", false,
		"Treat the seeded data in the database as stale: rewrite every declared column, and report no conflicts")

	var diffNoValues bool

	entityDiffCmd := &cobra.Command{
		Use:         "diff [file]",
		Short:       "Line an entity file's declared graph up against the rows joka tracks and the rows in the database",
		Annotations: needs("entities", nil),
		Args:        cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return entity.RunEntityDiffCommand{
				DB:           dbConn,
				EntitiesDir:  entitiesDir,
				FilePath:     args[0],
				SkipValues:   diffNoValues,
				OutputFormat: outputFormat,
			}.Execute(c.Context())
		},
	}

	entityDiffCmd.Flags().BoolVar(&diffNoValues, "no-values", false, "Skip the per-row column comparison (saves one query per matched row)")

	dropCmd := &cobra.Command{
		Use:         "drop",
		Short:       "Drop every table in the database (including joka_* tracking)",
		Annotations: wipes,
		RunE: func(c *cobra.Command, _ []string) error {
			return dbtools.RunDropCommand{
				DB:           dbConn,
				AutoConfirm:  autoConfirm,
				OutputFormat: outputFormat,
			}.Execute(c.Context())
		},
	}

	resetCmd := &cobra.Command{
		Use:         "reset",
		Short:       "Drop everything and re-run init, migrations and entity sync",
		Annotations: needs("migrations,entities", wipes),
		RunE: func(c *cobra.Command, _ []string) error {
			return dbtools.RunResetCommand{
				DB:            dbConn,
				Secrets:       secrets.New(cfg.Secrets),
				MigrationsDir: migrationsDir,
				EntitiesDir:   entitiesDir,
				AutoConfirm:   autoConfirm,
				OutputFormat:  outputFormat,
				Profile:       profile,
				StateFile:     stateFile,
				JokaVersion:   version,
			}.Execute(c.Context())
		},
	}

	migrateCmd.AddCommand(migrateNewCmd, migrateUpCmd, migrateStatusCmd, migrateConsolidateCmd, migrateVerifyCmd)
	entityCmd.AddCommand(entitySyncCmd, entityDiffCmd)
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print the version number",
		Run: func(c *cobra.Command, _ []string) {
			if outputFormat == shared.OutputJSON {
				shared.PrintJSON(map[string]string{"version": version})
			} else {
				fmt.Println("joka", version)
			}
		},
	}

	root.AddCommand(initCmd, statusCmd, migrateCmd, entityCmd, dropCmd, resetCmd, unlockCmd, versionCmd)

	if err := root.Execute(); err != nil {
		switch {
		case outputFormat == shared.OutputJSON:
			shared.PrintErrorJSON(err)
		case errors.Is(err, shared.ErrCancelled):
			// The command already said it stopped, in its own words. What it
			// still owes the shell is a non-zero exit, so a `&&` chain does not
			// carry on past a step the operator declined.
		default:
			color.Red("%v", err)
		}
		os.Exit(1)
	}
}

// loadEnv loads environment variables from the given .env file path. If the
// path is the default ".env" and the file doesn't exist, it silently continues.
func loadEnv(envFile string) error {
	if envFile != ".env" {
		if _, err := os.Stat(envFile); err != nil {
			return fmt.Errorf("unable to find specified .env file: %s", envFile)
		}
	}
	godotenv.Load(envFile)
	return nil
}

// refuseWrongDatabase stops a command that writes when the state file beside
// the working directory describes a database this is not.
//
// The check is here rather than in entity sync because every writing command
// has the same exposure: `migrate up` against a database nobody meant to touch
// is as bad as a sync against one. It runs on the same annotation as the
// upgrade gate, so `drop` and `reset` skip it — they destroy the tracking, and
// being unable to reset a database because its state file is stale is the wrong
// way round.
//
// Only a disagreeing identity refuses. See StateAudit.BlocksWrite.
func refuseWrongDatabase(c *cobra.Command, stateFile, profile string, db *sql.DB) error {
	path := entityinfra.StateFilePath(stateFile, profile)

	doc, hasFile, err := entityinfra.ReadStateFile(path)
	if err != nil {
		return err
	}

	metaState, err := meta.Read(c.Context(), db)
	if err != nil {
		return err
	}

	audit := entityapp.AuditState(hasFile, doc.Identity, doc.Version,
		metaState.StateIdentity, metaState.StateVersion)
	if !audit.BlocksWrite() {
		return nil
	}

	return fmt.Errorf("%w: %s names %s, and this database is %s. Nothing was written.\n"+
		"  If the connection is right, the state file is stale: remove it, or point --statefile "+
		"somewhere else.",
		domain.ErrWrongDatabase, path,
		identityOrNone(doc.Identity), identityOrNone(metaState.StateIdentity))
}

// identityOrNone renders a state identity for the refusal above. An empty one
// is a database joka has never written state to, which reads better as a phrase
// than as an empty pair of quotes.
func identityOrNone(identity string) string {
	if identity == "" {
		return "a database joka has never written state to"
	}
	return identity
}

// requireDirs checks the directories a command declared it reads, in the order
// a command that reads both would reach them.
func requireDirs(c *cobra.Command, migrationsDir, entitiesDir string, cfg *config.Config) error {
	for _, kind := range strings.Split(c.Annotations[annotationNeeds], ",") {
		switch kind {
		case "migrations":
			if err := shared.RequireDir(kind, migrationsDir, dirSource(c, kind, cfg.Migrations)); err != nil {
				return err
			}
		case "entities":
			if err := shared.RequireDir(kind, entitiesDir, dirSource(c, kind, cfg.Entities)); err != nil {
				return err
			}
		}
	}

	return nil
}

// dirSource says who named a directory. A path somebody named and got wrong is
// a different mistake from a default that was never right.
func dirSource(c *cobra.Command, flag, configured string) shared.DirSource {
	switch {
	case c.Flags().Changed(flag):
		return shared.DirFlag
	case configured != "":
		return shared.DirConfig
	}

	return shared.DirDefault
}

// refuseWrongRoot stops a command that writes when the database was claimed by
// a joka root other than the one running.
//
// It sits beside refuseWrongDatabase and covers the case that one cannot. The
// state file carries the identity of the database it describes, which catches a
// directory pointed at the wrong database — but the file is generally
// gitignored, so a CI checkout has none and the audit returns no_file. The root
// claim lives in a committed configuration instead, so it is there on a fresh
// clone, which is exactly where the damage was done.
//
// Like the upgrade gate it runs on the mutates annotation, so drop and reset
// skip it: they destroy the tracking, and being unable to reset a database
// because of who owns its bookkeeping is the wrong way round.
func refuseWrongRoot(c *cobra.Command, db *sql.DB, declared string, adopt bool) error {
	state, err := meta.Read(c.Context(), db)
	if err != nil {
		return err
	}

	return meta.CheckRoot(state.StateRoot, declared, adopt)
}
