// Package jokalib runs joka's operations against a database from Go, so a project
// can migrate and seed in its own tests and boot code without shelling out to
// the binary and without reimplementing what the binary does.
//
// It exists because reimplementing it went wrong in practice. jjc2's test
// helpers wrote their own migration splitter, and it disagreed with joka's
// about semicolons inside comments — so a migration the tool applied cleanly
// failed under test, and the rule the team wrote down ("no ; in migration
// comments") described their splitter rather than joka's. Tests should run the
// code the tool runs.
//
// The API is deliberately small. Every function here is a thin call onto the
// same command the CLI invokes, with the CLI's concerns — prompting, output
// format, exit codes — fixed at the values a program wants: never prompt, and
// write nothing unless asked.
//
// Naming: the binary is package main at the module root, so the library lives
// one level down. Moving main would change `go install
// github.com/apsdsm/joka@latest`, which consuming projects already have in
// their Dockerfiles. It is not called "joka" because `go build .` writes a
// binary of that name into the repo root and cannot, if a directory is already
// sitting there. Alias it at the import if the call site reads better that way:
//
//	import joka "github.com/apsdsm/joka/jokalib"
//
//	joka.MigrateUp(ctx, db, "devops/migrations")
package jokalib

import (
	"context"
	"database/sql"
	"io"

	"github.com/apsdsm/joka/cmd/entity"
	"github.com/apsdsm/joka/cmd/migration"
	entityapp "github.com/apsdsm/joka/internal/domains/entity/app"
)

// options carries what a caller may vary. Everything else is fixed, because a
// library has no terminal to prompt at and no exit code to set.
type options struct {
	output   io.Writer
	skipLock bool
}

// An Option varies how an operation runs.
type Option func(*options)

// WithOutput sends progress to w. The default is io.Discard: a package a test
// helper calls has no business writing to the process stdout.
//
// EntitySync does not honour it yet — see its own documentation.
func WithOutput(w io.Writer) Option {
	return func(o *options) { o.output = w }
}

// WithoutLock skips the advisory lock.
//
// The lock is worth holding against a shared database and is pure cost against
// a container one test owns, where nothing else can be connected. It is on by
// default anyway: a caller who knows the database is private says so, rather
// than everyone else being unprotected by default.
func WithoutLock() Option {
	return func(o *options) { o.skipLock = true }
}

func resolve(opts []Option) options {
	o := options{output: io.Discard}
	for _, apply := range opts {
		apply(&o)
	}
	return o
}

// Init creates the joka_migrations table. It is safe to call on a database that
// already has one.
func Init(ctx context.Context, db *sql.DB, opts ...Option) error {
	o := resolve(opts)

	return migration.RunInitCommand{
		DB:           db,
		OutputFormat: "text",
		Output:       o.output,
	}.Execute(ctx)
}

// MigrateUp applies every pending migration in migrationsDir, in one
// transaction, exactly as `joka migrate up` does — same file ordering, same
// statement splitting, same snapshot capture.
func MigrateUp(ctx context.Context, db *sql.DB, migrationsDir string, opts ...Option) error {
	o := resolve(opts)

	return migration.RunMigrateUpCommand{
		DB:            db,
		MigrationsDir: migrationsDir,
		AutoConfirm:   true,
		OutputFormat:  "text",
		SkipLock:      o.skipLock,
		Output:        o.output,
	}.Execute(ctx)
}

// EntitySync applies the seed files in entitiesDirs, as `joka entity sync`
// does. Several directories are synced as one desired state, and a reference
// resolves across all of them.
//
// Two differences from the other functions in this package, both of which
// should go:
//
// It writes its plan to stdout and ignores WithOutput. The sync command prints
// from eighty-eight places, against nine in migrate up, so threading a writer
// through it is its own change rather than a detail of this one. Under `go
// test` the output is buffered and shown only for a failing test, which is why
// this was worth shipping rather than holding.
//
// It deletes rows that no file declares, because it is given AllowDelete:
// there is nobody to confirm with. That is the same bargain `joka reset`
// makes. Point it at a database whose seed files are the whole truth.
//
// A conflict — the database holding a value joka did not write — is an error
// and nothing is applied, which is the CLI default. Deciding which side wins
// is a judgement, not something this package should make for a caller; use the
// CLI, where --on-conflict can be answered.
func EntitySync(ctx context.Context, db *sql.DB, entitiesDirs []string, opts ...Option) error {
	o := resolve(opts)

	return entity.RunEntitySyncCommand{
		DB:           db,
		EntitiesDirs: entitiesDirs,
		AutoConfirm:  true,
		OutputFormat: "text",
		SkipLock:     o.skipLock,
		AllowDelete:  true,
		OnConflict:   entityapp.ConflictFail,
	}.Execute(ctx)
}
