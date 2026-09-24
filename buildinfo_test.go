package main

import (
	"runtime/debug"
	"strings"
	"testing"
)

func info(version string, settings ...debug.BuildSetting) *debug.BuildInfo {
	return &debug.BuildInfo{
		Main:     debug.Module{Version: version},
		Settings: settings,
	}
}

func TestVersionFrom(t *testing.T) {
	t.Run("a released build reports the tag", func(t *testing.T) {
		if got := versionFrom(info("v0.14.0")); got != "0.14.0" {
			t.Errorf("expected 0.14.0, got %q", got)
		}
	})

	t.Run("a pseudo-version is not a release", func(t *testing.T) {
		// `go build` in a tagged repo synthesises one, and it names a release
		// that does not exist while reading exactly like one that does.
		got := versionFrom(info("v0.14.1-0.20260924055729-20c4d2cb850e+dirty",
			debug.BuildSetting{Key: "vcs.revision", Value: "20c4d2cb850e0000000000000000000000000000"}))
		if !strings.HasPrefix(got, release+"+dev.") {
			t.Errorf("expected a dev version, got %q", got)
		}
		if strings.Contains(got, "0.14.1") {
			t.Errorf("expected no invented release number, got %q", got)
		}
	})

	t.Run("a local build names its commit", func(t *testing.T) {
		got := versionFrom(info("(devel)",
			debug.BuildSetting{Key: "vcs.revision", Value: "abcdef0123456789abcdef"}))
		if got != release+"+dev.abcdef012345" {
			t.Errorf("expected the short revision, got %q", got)
		}
	})

	t.Run("a dirty tree says so", func(t *testing.T) {
		// The revision names a commit this binary is not.
		got := versionFrom(info("(devel)",
			debug.BuildSetting{Key: "vcs.revision", Value: "abcdef0123456789"},
			debug.BuildSetting{Key: "vcs.modified", Value: "true"}))
		if !strings.HasSuffix(got, ".dirty") {
			t.Errorf("expected a dirty marker, got %q", got)
		}
	})

	t.Run("no tag and no VCS says unknown rather than claiming the release", func(t *testing.T) {
		if got := versionFrom(info("(devel)")); got != release+"+unknown" {
			t.Errorf("expected an unknown marker, got %q", got)
		}
	})

	t.Run("a release and a dev build are never the same string", func(t *testing.T) {
		// The whole point: joka writes this into joka_meta, so two builds that
		// report the same thing make joka_version useless.
		released := versionFrom(info("v0.14.0"))
		dev := versionFrom(info("(devel)",
			debug.BuildSetting{Key: "vcs.revision", Value: "abcdef0123456789"}))
		if released == dev {
			t.Error("expected a released build and a dev build to report differently")
		}
	})
}
