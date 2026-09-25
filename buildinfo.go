package main

import (
	"regexp"
	"runtime/debug"
	"strings"
)

// release is the version this source is. Bumping it and tagging is the release
// procedure; see CLAUDE.md.
const release = "0.16.0"

// version is what joka reports, and what it stamps into joka_meta as
// joka_version: `release` for a build made from a released version, and
// release plus the commit for anything else.
//
// A build that cannot say which build it is was a real problem rather than a
// cosmetic one, because joka writes this into every database it touches. A dev
// binary and the release it was branched from both reported "0.14.0", so
// joka_meta could not say which had last written and neither could anyone
// comparing two environments.
//
// It comes from debug.BuildInfo rather than from -ldflags, because
// `go install module@version` — which is how consuming projects' images get
// joka — accepts no linker flags. BuildInfo is there either way: a released
// build carries its module version, and a build from a working tree carries
// the VCS revision and whether the tree was dirty.
var version = buildVersion()

// releaseTag matches a plain tagged version and nothing else: not a
// pseudo-version, not a pre-release.
var releaseTag = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

func buildVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return release
	}

	return versionFrom(info)
}

// versionFrom is buildVersion over a BuildInfo it is given, so the four cases
// can be tested without four different builds.
func versionFrom(info *debug.BuildInfo) string {

	// Only a clean tag counts. `go install module@v0.14.0` records exactly
	// that, but a local build records either "(devel)" or a pseudo-version
	// synthesised from the VCS — "0.14.1-0.20260924055729-20c4d2cb850e+dirty",
	// which names a release that does not exist and reads as one that does.
	if releaseTag.MatchString(info.Main.Version) {
		return strings.TrimPrefix(info.Main.Version, "v")
	}

	var revision, modified string
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}

	if revision == "" {
		// No tag and no VCS: `go run`, or a build from a tree with no
		// repository. Saying so is better than claiming the release.
		return release + "+unknown"
	}

	if len(revision) > 12 {
		revision = revision[:12]
	}

	suffix := "+dev." + revision
	if modified == "true" {
		// Uncommitted changes: the revision names a commit this binary is not.
		suffix += ".dirty"
	}

	return release + suffix
}
