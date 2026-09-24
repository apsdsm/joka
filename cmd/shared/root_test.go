package shared

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequireDir(t *testing.T) {
	t.Run("it passes when the directory is there", func(t *testing.T) {
		dir := t.TempDir()
		if err := RequireDir("entities", dir, DirDefault); err != nil {
			t.Fatalf("expected no error for an existing directory, got: %v", err)
		}
	})

	t.Run("it refuses a file standing where a directory should be", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "entities")
		if err := os.WriteFile(path, []byte("not a directory"), 0644); err != nil {
			t.Fatalf("writing the file: %v", err)
		}

		if err := RequireDir("entities", path, DirDefault); err == nil {
			t.Fatal("expected a file to be refused, got nil")
		}
	})

	t.Run("a missing default directory reads as the wrong working directory", func(t *testing.T) {
		err := RequireDir("entities", "devops/entities", DirDefault)
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
		if !errors.Is(err, ErrNotJokaDir) {
			t.Errorf("expected ErrNotJokaDir, got: %v", err)
		}
		// The point of the message is that it names what is missing and what to
		// do, rather than only the path joka happened to try.
		for _, want := range []string{ConfigFile, "devops/entities", "--entities"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("expected the refusal to mention %q, got: %v", want, err)
			}
		}
	})

	t.Run("a missing configured directory is a plain not-found", func(t *testing.T) {
		err := RequireDir("entities", "./seeds", DirConfig)
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
		// The author said where to look, so telling them this is not a joka
		// directory would be answering a question they did not ask.
		if errors.Is(err, ErrNotJokaDir) {
			t.Error("a directory the config named should not read as the wrong directory")
		}
		if !strings.Contains(err.Error(), ConfigFile) {
			t.Errorf("expected the error to say the config named it, got: %v", err)
		}
	})

	t.Run("a missing flagged directory names the flag", func(t *testing.T) {
		err := RequireDir("migrations", "/nope", DirFlag)
		if err == nil {
			t.Fatal("expected an error, got nil")
		}
		if errors.Is(err, ErrNotJokaDir) {
			t.Error("a directory a flag named should not read as the wrong directory")
		}
		if !strings.Contains(err.Error(), "--migrations") {
			t.Errorf("expected the error to name --migrations, got: %v", err)
		}
	})
}
