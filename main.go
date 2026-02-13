package main

import (
	"database/sql"
	"fmt"
	"os"

	"github.com/fatih/color"
	"github.com/joho/godotenv"
	"github.com/apsdsm/joka/cmd/lock"
	"github.com/apsdsm/joka/cmd/migration"
	"github.com/apsdsm/joka/cmd/template"
	"github.com/apsdsm/joka/config"
	templateinfra "github.com/apsdsm/joka/internal/domains/template/infra"
	jokadb "github.com/apsdsm/joka/db"
	"github.com/spf13/cobra"
)

const version = "0.1.0"

func main() {
	var (
		envFile       string
		migrationsDir string
		templatesDir  string
		autoConfirm   bool
		dbConn        *sql.DB
		cfg           *config.Config
	)

	root := &cobra.Command{
		Use:   "joka",
		Short: "MySQL migration management tool",
		PersistentPreRunE: func(c *cobra.Command, args []string) error {
			var err error
			cfg, err = config.Load()
			if err != nil {
				return err
			}

			if !c.Flags().Changed("migrations") && cfg.Migrations != "" {
				migrationsDir = cfg.Migrations
			}
			if !c.Flags().Changed("templates") && cfg.Templates != "" {
				templatesDir = cfg.Templates
			}

			if c.Name() == "version" {
				return nil
			}

			if c.Name() == "make" {
				return loadEnv(envFile)
			}

			if err := loadEnv(envFile); err != nil {
				return err
			}

			dsn := os.Getenv("DATABASE_URL")
			if dsn == "" {
				return fmt.Errorf("DATABASE_URL not found in environment variables")
			}

			dbConn, err = jokadb.Open(dsn)
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
	root.PersistentFlags().StringVarP(&migrationsDir, "migrations", "m", "devops/migrations", "Path to the migrations directory")
	root.PersistentFlags().StringVarP(&templatesDir, "templates", "t", "devops/templates", "Path to the templates directory")
	root.PersistentFlags().BoolVarP(&autoConfirm, "auto", "a", false, "Automatically confirm prompts")

	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize the migrations table",
		RunE: func(c *cobra.Command, _ []string) error {
			return migration.RunInitCommand{DB: dbConn}.Execute(c.Context())
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
				MigrationsDir: migrationsDir,
				AutoConfirm:   autoConfirm,
			}.Execute(c.Context())
		},
	}

	migrateStatusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show migration status",
		RunE: func(c *cobra.Command, _ []string) error {
			return migration.RunMigrateStatusCommand{
				DB:            dbConn,
				MigrationsDir: migrationsDir,
			}.Execute(c.Context())
		},
	}

	dataCmd := &cobra.Command{
		Use:   "data",
		Short: "Application data state commands",
	}

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
			return template.RunDataSyncCommand{
				DB:           dbConn,
				TemplatesDir: templatesDir,
				Tables:       tables,
				AutoConfirm:  autoConfirm,
			}.Execute(c.Context())
		},
	}

	unlockCmd := &cobra.Command{
		Use:   "unlock",
		Short: "Force-release a held lock",
		RunE: func(c *cobra.Command, _ []string) error {
			return lock.RunUnlockCommand{DB: dbConn}.Execute(c.Context())
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
				MigrationIndex: index,
			}.Execute(c.Context())
		},
	}

	migrateCmd.AddCommand(migrateUpCmd, migrateStatusCmd, migrateSnapshotCmd)
	dataCmd.AddCommand(dataSyncCmd)
	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print the version number",
		Run: func(c *cobra.Command, _ []string) {
			fmt.Println("joka", version)
		},
	}

	root.AddCommand(initCmd, makeCmd, migrateCmd, dataCmd, unlockCmd, versionCmd)

	if err := root.Execute(); err != nil {
		color.Red("%v", err)
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
