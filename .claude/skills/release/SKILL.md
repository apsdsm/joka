---
name: release
description: Run the full release-readiness workflow for joka — tests, vulnerability scan, doc check — then bump version, commit, tag, and push. Use when the user asks to "release", "cut a release", "ship a new version", "tag and push a release", or similar. Use even when the user names a specific version ("release v0.7.1") — the checklist still applies.
---

# Release a new version

Run this workflow before pushing a new tag. Every step is a gate: do not advance until the previous step is green. Do not skip steps for speed.

If at any point a step fails, **stop and fix the root cause** — never bypass with `--no-verify`, `--force`, skipped tests, or comments. Report failures clearly and ask the user how to proceed if the fix is non-obvious.

## Phase 1 — Verify state

1. Confirm we're on `main` with a clean tree:
   ```bash
   git status
   git log --oneline -5
   ```
   - If on a feature branch: ask whether to merge first or release from the branch.
   - If uncommitted changes exist: list them and ask whether to commit, stash, or abort.

2. Run the full test suite:
   ```bash
   go test ./... -count=1 -timeout 600s
   ```
   - **All packages must pass.** If any fail: investigate root cause, fix the code (don't disable tests), re-run.
   - Report the result explicitly: "Full suite green" or list failing packages.

3. Run the vulnerability scan:
   ```bash
   # Install if missing — binary lives at $(go env GOPATH)/bin or the toolchain bin dir
   go install golang.org/x/vuln/cmd/govulncheck@latest
   govulncheck ./...
   ```
   - **Must report "No vulnerabilities found."**
   - If vulns are found:
     - Direct dependency: `go get <module>@<fixed-version>`, then `go mod tidy`.
     - Indirect: bump the parent dep that pulls it in.
     - Standard library: pin `toolchain go1.X.Y` in `go.mod` (the fixed version from the advisory).
     - Re-run until clean. Re-run the test suite after dep bumps.
   - If a vuln has no fix yet: report it to the user and confirm release with the known vuln. Don't ship silently.

## Phase 2 — Documentation freshness

1. List what has changed since the last release tag:
   ```bash
   git log $(git describe --tags --abbrev=0)..HEAD --oneline
   ```
2. Read `CLAUDE.md` and `README.md`. For each commit since the last tag, check whether the docs reflect it. Specifically look for:
   - New commands or subcommands → must appear in the Commands/Examples section.
   - New CLI flags → must be documented.
   - New config knobs, file formats, table schemas → must be in the relevant section.
   - Removed/renamed features → must not still be advertised.
3. If docs are stale: update them now (same commit as the version bump), don't defer.

## Phase 3 — Decide version

Show the user the commit list from Phase 2 and propose a version following semver:

- **Patch** (`0.x.Y+1`) — bug fixes only, no new commands/flags/behavior.
- **Minor** (`0.X+1.0`) — new features or commands, backwards-compatible.
- **Major** (`X+1.0.0`) — breaking changes (command removed/renamed, flag semantics changed, schema migration required).

For pre-1.0 projects (joka is currently pre-1.0): minor bumps are fine for new commands; reserve major bumps for genuine breakage.

Confirm the version with the user before proceeding. State your recommendation and reasoning.

## Phase 4 — Bump, commit, tag, push

1. Update the version constant:
   ```go
   // main.go
   const version = "0.X.Y"
   ```
2. Stage and commit (use a HEREDOC for the message):
   ```bash
   git add main.go <any-doc-files-touched>
   git commit -m "$(cat <<'EOF'
   <one-line subject describing the headline change, ending with "bump to v0.X.Y">

   <optional body summarizing notable changes since last release>

   Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
   EOF
   )"
   ```
3. Tag and push:
   ```bash
   git tag v0.X.Y
   git push && git push origin v0.X.Y
   ```
4. Confirm:
   ```bash
   git log --oneline -3
   git tag --list 'v0.X.*' | tail -5
   ```

## Phase 5 — End-of-release summary

Tell the user:
- Commit SHA and message
- Tag name
- Confirmation that both pushed to `origin`
- Anything noteworthy from Phases 1-3 (e.g., "bumped 3 deps and toolchain for a stdlib CVE", "updated README to document `joka migrate verify`")

## Anti-patterns

- Skipping `go test ./...` because "I already ran it earlier this session". Re-run on the release commit.
- Pushing the commit before the tag, then forgetting the tag. Push both, then verify with `git tag --list`.
- Bumping the version without checking docs. If the user asks "did you update the README?" the answer should already be yes.
- `--no-verify`, `-c commit.gpgsign=false`, force-push to main, or any "make the gate stop complaining" shortcut. Fix the underlying issue.
- Tagging from a worktree branch. Merge to main first, then tag from main.
