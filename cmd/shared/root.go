package shared

import (
	"errors"
	"fmt"
	"os"
)

// ConfigFile is what marks a directory as one joka is meant to be run from.
const ConfigFile = ".jokarc.yaml"

// ErrNotJokaDir is returned when a command needs one of joka's directories, the
// directory is not there, and nothing in the working directory suggests joka
// was meant to run there at all.
var ErrNotJokaDir = errors.New("not a joka directory")

// DirSource says who named the path being checked. A directory somebody named
// and got wrong is a different mistake from a default that was never right,
// and the two want different sentences.
type DirSource int

const (
	// DirDefault: nobody named it, so joka fell back to its built-in default.
	DirDefault DirSource = iota
	// DirConfig: the .jokarc.yaml in the working directory named it.
	DirConfig
	// DirFlag: a command-line flag named it.
	DirFlag
)

// RequireDir refuses a command whose working directory does not hold a
// directory it needs.
//
// It has to run before the database connection is opened. joka opened one
// first, ran the tracking upgrade, and created joka_meta, joka_state and
// joka_lock — and only then read the directory and failed. A command run from
// the wrong place therefore wrote three tables into a database it was never
// meant to reach, which is the whole exposure of having a prod directory next
// to a dev one.
//
// Nothing marks a joka root on its own, so the working directory is the root
// and the absence of both the config file and the default directory is what
// says you are somewhere else. A path that was named — by the config or by a
// flag — is reported as a plain not-found, because the author said where to
// look and being told the directory is not a joka one would be wrong.
func RequireDir(kind, dir string, source DirSource) error {
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		return nil
	}

	wd, err := os.Getwd()
	if err != nil {
		wd = "the working directory"
	}

	switch source {
	case DirFlag:
		return fmt.Errorf("%s directory not found: %s\n  named by --%s", kind, dir, kind)
	case DirConfig:
		return fmt.Errorf("%s directory not found: %s\n  named by %s in %s", kind, dir, ConfigFile, wd)
	}

	return fmt.Errorf("%w: %s\n"+
		"  it holds no %s, and the default %s directory (%s) is not there\n"+
		"  run joka from the directory that holds its config, or pass --%s",
		ErrNotJokaDir, wd, ConfigFile, kind, dir, kind)
}
