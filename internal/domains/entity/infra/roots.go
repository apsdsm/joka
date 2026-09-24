package infra

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// DiscoveredFile is one seed file found under one of the entity roots.
type DiscoveredFile struct {
	// Key is what the state records and what an entity's File field holds. It
	// is the path relative to the root for a single root, so every project
	// that has one keeps the keys it already has, and the root-prefixed path
	// when there are several, because two roots may each hold an admin.yaml
	// and one key cannot mean both.
	Key string
	// Full is where the file actually is, for reading it and for writing it
	// back under --on-conflict=ask. Derived once here rather than rebuilt by
	// joining a root onto a key, which stops working the moment the key
	// carries a root of its own.
	Full string
}

// DiscoverEntityRoots walks every root in order and returns the files in them.
//
// Order is the declared order of the roots, then each root's own walk order.
// It is only a starting point: the planner reorders by reference so a file
// declaring an _id comes before one that references it, which is what makes a
// seed in one root able to point at a seed in another.
func DiscoverEntityRoots(roots []string) ([]DiscoveredFile, error) {
	if err := checkRoots(roots); err != nil {
		return nil, err
	}

	prefix := len(roots) > 1
	var out []DiscoveredFile

	for _, root := range roots {
		rels, err := DiscoverEntityFiles(root)
		if err != nil {
			return nil, err
		}

		for _, rel := range rels {
			key := rel
			if prefix {
				key = filepath.ToSlash(filepath.Join(filepath.Clean(root), rel))
			}

			out = append(out, DiscoveredFile{Key: key, Full: filepath.Join(root, rel)})
		}
	}

	if err := checkKeys(out); err != nil {
		return nil, err
	}

	return out, nil
}

// checkRoots refuses a list that names the same directory twice. Loading a
// root twice would declare every _id in it twice, which the set validator
// would then report as a duplicate — an error about the seed files for a
// mistake in the configuration.
func checkRoots(roots []string) error {
	if len(roots) == 0 {
		return fmt.Errorf("no entities directory configured")
	}

	seen := make(map[string]bool, len(roots))
	clean := make([]string, len(roots))

	for i, root := range roots {
		clean[i] = filepath.Clean(root)
		if seen[clean[i]] {
			return fmt.Errorf("the entities directory %q is listed more than once", clean[i])
		}
		seen[clean[i]] = true
	}

	// One root inside another means every file in the inner one is found twice
	// — once by each walk — so every _id in it is declared twice and the set
	// validator refuses, naming the same file under two paths. That message is
	// true but describes the symptom; this one describes the mistake.
	//
	// The comparison is on absolute paths, because relative ones cannot be
	// compared as strings: "." and "./entities" clean to "." and "entities",
	// which share no prefix, and "../shared" and "entities" may or may not
	// overlap depending on where the process is standing.
	abs := make([]string, len(clean))
	for i, c := range clean {
		a, err := filepath.Abs(c)
		if err != nil {
			// Without an absolute path there is nothing to compare. The
			// duplicate-_id refusal still catches the overlap, so this is a
			// worse message rather than no check.
			return nil
		}
		abs[i] = a
	}

	for i, outer := range abs {
		for j, inner := range abs {
			if i == j {
				continue
			}
			if strings.HasPrefix(inner, outer+string(filepath.Separator)) {
				return fmt.Errorf("the entities directory %q is inside %q, so its files would be loaded twice",
					clean[j], clean[i])
			}
		}
	}

	return nil
}

// checkKeys refuses two files that would share a state key. Distinct roots
// normally give distinct keys, but a root nested inside another does not — the
// same file is then found under both walks, and one key would have to mean two
// files.
func checkKeys(files []DiscoveredFile) error {
	seen := make(map[string]string, len(files))
	var clashes []string

	for _, f := range files {
		if first, ok := seen[f.Key]; ok {
			clashes = append(clashes, fmt.Sprintf("%s is both %s and %s", f.Key, first, f.Full))
			continue
		}
		seen[f.Key] = f.Full
	}

	if len(clashes) == 0 {
		return nil
	}

	sort.Strings(clashes)

	return fmt.Errorf("entity files collide (is one entities directory inside another?):\n  %s",
		strings.Join(clashes, "\n  "))
}
