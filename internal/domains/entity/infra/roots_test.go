package infra_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/apsdsm/joka/internal/domains/entity/infra"
)

func seedDir(t *testing.T, parent string, names ...string) string {
	t.Helper()

	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("creating %s: %v", parent, err)
	}
	for _, name := range names {
		path := filepath.Join(parent, name)
		if err := os.WriteFile(path, []byte("entities: []\n"), 0o644); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}

	return parent
}

func TestDiscoverEntityRoots(t *testing.T) {
	t.Run("one root keys files relative to it, as it always has", func(t *testing.T) {
		// Every project has one root today. Changing their keys would make
		// every file read as new on the next sync for no reason.
		dir := seedDir(t, filepath.Join(t.TempDir(), "entities"), "admin.yaml")

		found, err := infra.DiscoverEntityRoots([]string{dir})
		if err != nil {
			t.Fatalf("DiscoverEntityRoots: %v", err)
		}
		if len(found) != 1 || found[0].Key != "admin.yaml" {
			t.Fatalf("expected the bare relative key, got %+v", found)
		}
	})

	t.Run("several roots prefix the key, so two admin.yaml can coexist", func(t *testing.T) {
		base := t.TempDir()
		shared := seedDir(t, filepath.Join(base, "shared"), "admin.yaml")
		local := seedDir(t, filepath.Join(base, "local"), "admin.yaml")

		found, err := infra.DiscoverEntityRoots([]string{shared, local})
		if err != nil {
			t.Fatalf("DiscoverEntityRoots: %v", err)
		}
		if len(found) != 2 {
			t.Fatalf("expected two files, got %d", len(found))
		}
		if found[0].Key == found[1].Key {
			t.Errorf("expected distinct keys, both are %q", found[0].Key)
		}
		for _, f := range found {
			if !strings.HasSuffix(f.Full, "admin.yaml") {
				t.Errorf("expected Full to point at the file, got %q", f.Full)
			}
		}
	})

	t.Run("roots are walked in declared order", func(t *testing.T) {
		base := t.TempDir()
		second := seedDir(t, filepath.Join(base, "zzz"), "a.yaml")
		first := seedDir(t, filepath.Join(base, "aaa"), "a.yaml")

		found, err := infra.DiscoverEntityRoots([]string{second, first})
		if err != nil {
			t.Fatalf("DiscoverEntityRoots: %v", err)
		}
		if !strings.Contains(found[0].Full, "zzz") {
			t.Errorf("expected the declared order, got %q first", found[0].Full)
		}
	})

	t.Run("a dotfile is configuration, not a seed", func(t *testing.T) {
		// .jokarc.yaml matches *.yaml as readily as anything else, and an
		// entities root that sits beside a config — or contains one — had it
		// parsed as an entity file.
		dir := seedDir(t, filepath.Join(t.TempDir(), "entities"), "admin.yaml")
		if err := os.WriteFile(filepath.Join(dir, ".jokarc.yaml"), []byte("entities: x\n"), 0o644); err != nil {
			t.Fatalf("writing the config: %v", err)
		}

		found, err := infra.DiscoverEntityRoots([]string{dir})
		if err != nil {
			t.Fatalf("DiscoverEntityRoots: %v", err)
		}
		if len(found) != 1 {
			t.Fatalf("expected the dotfile to be skipped, got %+v", found)
		}
	})

	t.Run("the same root twice is refused", func(t *testing.T) {
		dir := seedDir(t, filepath.Join(t.TempDir(), "entities"), "admin.yaml")

		_, err := infra.DiscoverEntityRoots([]string{dir, dir})
		if err == nil || !strings.Contains(err.Error(), "more than once") {
			t.Fatalf("expected a duplicate-root refusal, got: %v", err)
		}
	})

	t.Run("a root inside another is refused, and says so", func(t *testing.T) {
		base := t.TempDir()
		outer := seedDir(t, filepath.Join(base, "entities"), "admin.yaml")
		inner := seedDir(t, filepath.Join(outer, "local"), "extra.yaml")

		_, err := infra.DiscoverEntityRoots([]string{outer, inner})
		if err == nil || !strings.Contains(err.Error(), "is inside") {
			t.Fatalf("expected a nesting refusal, got: %v", err)
		}
	})

	t.Run("no roots at all is refused", func(t *testing.T) {
		if _, err := infra.DiscoverEntityRoots(nil); err == nil {
			t.Fatal("expected an error for no configured directory")
		}
	})
}
