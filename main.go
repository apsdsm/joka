package main

import (
	"database/sql"
	"fmt"
	"os"

	"github.com/fatih/color"
	"github.com/joho/godotenv"
	"github.com/apsdsm/joka/cmd/dbtools"
	"github.com/apsdsm/joka/cmd/entity"
	"github.com/apsdsm/joka/cmd/lock"
	"github.com/apsdsm/joka/cmd/migration"
	"github.com/apsdsm/joka/cmd/shared"
	"github.com/apsdsm/joka/cmd/template"
	"github.com/apsdsm/joka/config"
	"github.com/apsdsm/joka/internal/connection"
	"github.com/apsdsm/joka/internal/secrets"
	templateinfra "github.com/apsdsm/joka/internal/domains/template/infra"
	jokadb "github.com/apsdsm/joka/db"
	"github.com/spf13/cobra"
)

const version = "0.12.0"

func main() {
	var (
		envFile       string
		profile       string
		migrationsDir string
		templatesDir  string
		entitiesDir   string
		autoConfirm   bool
		outputFormat  string
		dbConn        *sql.DB
		dbDriver      jokadb.Driver
		cfg           *config.Config
	)

	root := &cobra.Command{
		Use:   "joka",
		Short: "Database migration management tool",
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
			if !c.Flags().Changed("templates") && cfg.Templates != "" {
				templatesDir = cfg.Templates
			}
			if !c.Flags().Changed("entities") && cfg.Entities != "" {
				entitiesDir = cfg.Entities
			}

			if c.Name() == "version" {
				return nil
			}

			// Load any --env dotenv first so the "env" connection source (and
			// anything else relying on process env) sees it.
			if err := loadEnv(envFile); err != nil {
				return err
			}

			if c.Name() == "make" {
				return nil
			}

			// Resolve the DSN from the connection config (env by default, or a
			// secret source declared in .jokarc.yaml / the selected profile).
			dsn, err := connection.Resolve(c.Context(), cfg.Connection, nil)
			if err != nil {
				return err
			}

			dbConn, dbDriver, err = jokadb.Open(dsn)
			if err != nil {
				return fmt.Errorf("error connecting to database: %w", err)
			}

			return nil
		},
		PersistentPostRun: func(c *cobra.Command, args []string) {
			if dbConn != nil {
				dbConn.Close()
			}
		},
	}

	root.PersistentFlags().StringVarP(&envFile, "env", "e", ".env", "Path to the environment file")
	root.PersistentFlags().StringVarP(&profile, "profile", "p", "", "Config profile to use (from .jokarc.yaml profiles)")
	root.PersistentFlags().StringVarP(&migrationsDir, "migrations", "m", "devops/migrations", "Path to the migrations directory")
	root.PersistentFlags().StringVarP(&templatesDir, "templates", "t", "devops/templates", "Path to the templates directory")
	root.PersistentFlags().StringVar(&entitiesDir, "entities", "devops/entities", "Path to the entities directory")
	root.PersistentFlags().BoolVarP(&autoConfirm, "auto", "a", false, "Automatically confirm prompts")
	root.PersistentFlags().StringVarP(&outputFormat, "output", "o", "text", "Output format: text or json")

	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize the migrations table",
		RunE: func(c *cobra.Command, _ []string) error {
			return migration.RunInitCommand{DB: dbConn, Driver: dbDriver, OutputFormat: outputFormat}.Execute(c.Context())
		},
	}

	makeCmd := &cobra.Command{
		Use:   "make [name]",
		Short: "Create a new migration file",
		Args:  cobra.ExactArgs(1),
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
		Use:   "up",
		Short: "Apply pending migrations",
		RunE: func(c *cobra.Command, _ []string) error {
			return migration.RunMigrateUpCommand{
				DB:            dbConn,
				Driver:        dbDriver,
				MigrationsDir: migrationsDir,
				AutoConfirm:   autoConfirm,
				OutputFormat:  outputFormat,
			}.Execute(c.Context())
		},
	}

	migrateStatusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show migration status",
		RunE: func(c *cobra.Command, _ []string) error {
			return migration.RunMigrateStatusCommand{
				DB:            dbConn,
				Driver:        dbDriver,
				MigrationsDir: migrationsDir,
				OutputFormat:  outputFormat,
			}.Execute(c.Context())
		},
	}

	dataCmd := &cobra.Command{
		Use:   "data",
		Short: "Application data state commands",
	}

	var ignoreForeignKeys bool

	dataSyncCmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync template data to the database",
		RunE: func(c *cobra.Command, _ []string) error {
			tables := make([]templateinfra.TableConfig, len(cfg.Tables))
			for i, t := range cfg.Tables {
				tables[i] = templateinfra.TableConfig{
					Name:     t.Name,
					Strategy: t.Strategy,
				}
			}

			// CLI flag overrides config; config is the default.
			ignoreFK := cfg.IgnoreForeignKeys
			if c.Flags().Changed("ignore-foreign-keys") {
				ignoreFK = ignoreForeignKeys
			}

			return template.RunDataSyncCommand{
				DB:                dbConn,
				Driver:            dbDriver,
				TemplatesDir:      templatesDir,
				Tables:            tables,
				AutoConfirm:       autoConfirm,
				IgnoreForeignKeys: ignoreFK,
				OutputFormat:      outputFormat,
			}.Execute(c.Context())
		},
	}

	dataSyncCmd.Flags().BoolVar(&ignoreForeignKeys, "ignore-foreign-keys", false, "Disable foreign key checks during truncate (MySQL)")

	unlockCmd := &cobra.Command{
		Use:   "unlock",
		Short: "Force-release a held lock",
		RunE: func(c *cobra.Command, _ []string) error {
			return lock.RunUnlockCommand{DB: dbConn, Driver: dbDriver, OutputFormat: outputFormat}.Execute(c.Context())
		},
	}

	migrateSnapshotCmd := &cobra.Command{
		Use:   "snapshot [migration_index]",
		Short: "View schema snapshot for a migration",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			var index string
			if len(args) > 0 {
				index = args[0]
			}
			return migration.RunSnapshotCommand{
				DB:             dbConn,
				Driver:         dbDriver,
				MigrationIndex: index,
				OutputFormat:   outputFormat,
			}.Execute(c.Context())
		},
	}

	migrateConsolidateCmd := &cobra.Command{
		Use:   "consolidate",
		Short: "Consolidate migrations into a single file using a schema snapshot",
		RunE: func(c *cobra.Command, _ []string) error {
			upTo, _ := c.Flags().GetString("up-to")
			if upTo == "" {
				return fmt.Errorf("--up-to flag is required")
			}
			return migration.RunConsolidateCommand{
				DB:            dbConn,
				Driver:        dbDriver,
				MigrationsDir: migrationsDir,
				UpToIndex:     upTo,
				AutoConfirm:   autoConfirm,
				OutputFormat:  outputFormat,
			}.Execute(c.Context())
		},
	}
	migrateConsolidateCmd.Flags().String("up-to", "", "Migration index to consolidate up to (required)")

	migrateVerifyCmd := &cobra.Command{
		Use:   "verify",
		Short: "Detect schema drift against the latest snapshot",
		RunE: func(c *cobra.Command, _ []string) error {
			return migration.RunVerifyCommand{
				DB:           dbConn,
				Driver:       dbDriver,
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

New files have their entity graph inserted. Files whose content changed since
the last sync (shown as [modified] by 'entity status') are reconciled in place:
each entity is updated by primary key against the tracked row at the same
depth-first position, so existing PKs are preserved (no delete, no FK conflict).
Unchanged files are skipped.

If a modified file changed structurally — a different number of entities than
tracked, an entity's table changed, or an _id that disagrees with the tracked
row at that position — sync refuses to guess and recommends 'entity reimport'.

Use --dry-run to print the planned inserts and before/after field changes
without applying anything.

Use --force to re-apply every tracked file's row updates regardless of its
stored hash. This is the escape hatch when change detection is in doubt.`,
		RunE: func(c *cobra.Command, _ []string) error {
			dryRun, _ := c.Flags().GetBool("dry-run")
			force, _ := c.Flags().GetBool("force")
			return entity.RunEntitySyncCommand{
				DB:           dbConn,
				Secrets:      secrets.New(cfg.Secrets),
				Driver:       dbDriver,
				EntitiesDir:  entitiesDir,
				AutoConfirm:  autoConfirm,
				OutputFormat: outputFormat,
				DryRun:       dryRun,
				Force:        force,
			}.Execute(c.Context())
		},
	}
	entitySyncCmd.Flags().Bool("dry-run", false, "Preview inserts and before/after changes without applying")
	entitySyncCmd.Flags().Bool("force", false, "Re-apply updates for every tracked file regardless of hash")

	entityStatusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show entity file sync status",
		RunE: func(c *cobra.Command, _ []string) error {
			return entity.RunEntityStatusCommand{
				DB:           dbConn,
				Driver:       dbDriver,
				EntitiesDir:  entitiesDir,
				OutputFormat: outputFormat,
			}.Execute(c.Context())
		},
	}

	entityReimportCmd := &cobra.Command{
		Use:   "reimport [file]",
		Short: "Re-sync an entity file (delete old rows, re-insert)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return entity.RunEntityReimportCommand{
				DB:           dbConn,
				Secrets:      secrets.New(cfg.Secrets),
				Driver:       dbDriver,
				EntitiesDir:  entitiesDir,
				FilePath:     args[0],
				AutoConfirm:  autoConfirm,
				OutputFormat: outputFormat,
			}.Execute(c.Context())
		},
	}

	entityUpdateCmd := &cobra.Command{
		Use:   "update [file]",
		Short: "Add new entities from a file without deleting existing rows",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return entity.RunEntityUpdateCommand{
				DB:           dbConn,
				Secrets:      secrets.New(cfg.Secrets),
				Driver:       dbDriver,
				EntitiesDir:  entitiesDir,
				FilePath:     args[0],
				AutoConfirm:  autoConfirm,
				OutputFormat: outputFormat,
			}.Execute(c.Context())
		},
	}

	dropCmd := &cobra.Command{
		Use:   "drop",
		Short: "Drop every table in the database (including joka_* tracking)",
		RunE: func(c *cobra.Command, _ []string) error {
			return dbtools.RunDropCommand{
				DB:           dbConn,
				Driver:       dbDriver,
				AutoConfirm:  autoConfirm,
				OutputFormat: outputFormat,
			}.Execute(c.Context())
		},
	}

	resetCmd := &cobra.Command{
		Use:   "reset",
		Short: "Drop everything and re-run init, migrations, data sync, entity sync",
		RunE: func(c *cobra.Command, _ []string) error {
			tables := make([]templateinfra.TableConfig, len(cfg.Tables))
			for i, t := range cfg.Tables {
				tables[i] = templateinfra.TableConfig{
					Name:     t.Name,
					Strategy: t.Strategy,
				}
			}

			return dbtools.RunResetCommand{
				DB:                dbConn,
				Secrets:           secrets.New(cfg.Secrets),
				Driver:            dbDriver,
				MigrationsDir:     migrationsDir,
				TemplatesDir:      templatesDir,
				EntitiesDir:       entitiesDir,
				Tables:            tables,
				IgnoreForeignKeys: cfg.IgnoreForeignKeys,
				AutoConfirm:       autoConfirm,
				OutputFormat:      outputFormat,
			}.Execute(c.Context())
		},
	}

	migrateCmd.AddCommand(migrateUpCmd, migrateStatusCmd, migrateSnapshotCmd, migrateConsolidateCmd, migrateVerifyCmd)
	dataCmd.AddCommand(dataSyncCmd)
	entityCmd.AddCommand(entitySyncCmd, entityStatusCmd, entityReimportCmd, entityUpdateCmd)
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

	root.AddCommand(initCmd, makeCmd, migrateCmd, dataCmd, entityCmd, dropCmd, resetCmd, unlockCmd, versionCmd)

	if err := root.Execute(); err != nil {
		if outputFormat == shared.OutputJSON {
			shared.PrintErrorJSON(err)
		} else {
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
