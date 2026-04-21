# Feature workflow

Direct pushes to `main` are reserved for trivial changes (typos, plan-doc tweaks, `.gitignore`, README edits — things that can't plausibly affect integration). Anything else goes through a feature branch and a pull request.

**Use a git worktree per feature.** Keep the `main` checkout clean for reviewing / bisecting / cherry-picking, and let each in-flight feature live in its own directory:

```
git worktree add .claude/worktrees/<feature> -b feat/<feature> main
```

Worktrees live under `.claude/worktrees/` (already gitignored). One feature per worktree, one branch per feature. When the feature lands, `git worktree remove` to reclaim the directory.

**Enable the pre-commit hook once per clone** with `make install-hooks` (sets `core.hooksPath = .githooks`, so every worktree inherits it). The hook runs `gofmt -w` and `golangci-lint run --fix --fast-only` on staged Go files, re-stages anything it rewrites, and fails only when issues remain after auto-fix. Heavy linters still run in CI.

Why worktrees instead of `git checkout` in a single tree:
- `main` stays immediately reviewable at HEAD — no stashing dance when a hotfix interrupts the feature.
- Parallel Claude Code sessions can work on independent features without fighting for the working tree.
- `git add -A` from the main tree no longer accidentally stages the worktree dir as a submodule pointer, since it's gitignored.

CI shape pairs with this:
- PRs and `main` pushes both run `test` (unit + lint + vulncheck, under a minute) and `integration` (real devpod + docker, ~2 min). Branch protection on `main` requires the branch to be up-to-date before merging, so a green PR reflects the actual post-merge state.
- Tag pushes run both via `release.yml` and gate goreleaser on the same steps — nothing ships that main hasn't already exercised.

# Release discipline

Never create or push a git tag unless the human explicitly asks for one in the current turn. Earlier approvals to tag (e.g. "tag v0.2.0") do not authorize follow-up tags — each release tag is its own explicit request.

A user saying "commit and push" does not imply tagging. A user saying "release" or "cut a release" does imply a tag, but confirm the version number before pushing.

# External repo references

Never reference other repositories, organisations, or user handles in anything that lands in this repo — commits, code, docs, plans, commit messages, tests, examples. Only this repo (`kurisu-agent/drift`) and its dependencies may appear. Unless the user explicitly requires it in the current turn, use generic placeholders (`example-org`, `<your-org>`, etc.) in examples and documentation.

# Termux/Android is a supported `drift` target

The `drift` client runs on Termux (Android) as a first-class platform — release tarballs ship `drift_<ver>_android_arm64.tar.gz`. When touching `drift` CLI code, assume the binary may be running on Termux and watch for these traps:

- **`os.Executable()` lies.** termux-exec runs every $PREFIX binary through the Android dynamic linker to bypass W^X SELinux. That makes `/proc/self/exe` (and thus `os.Executable()`) resolve to `/apex/com.android.runtime/bin/linker64` — not drift. Code that needs the running binary's real path must fall back to argv[0], which the linker preserves. See `resolveSelfPath` in `internal/cli/drift/update.go`.
- **`/apex` and `/system` are read-only.** Any write path derived from `os.Executable()` will hit EROFS on Android. Always anchor writes to `$HOME`, `$PREFIX`, or an explicit user-supplied path.
- **`exec` needs the linker wrap.** Don't call `os/exec` directly for binaries under `$PREFIX`; go through `internal/exec`, which handles the W^X escape hatch (`termuxLinkerWrap`).
- **No `/etc/resolv.conf`.** Go's pure-Go resolver fails when it's missing. Preserve the fallback wired up in `internal/cli/drift/dnsfix.go` (and the `DRIFT_DEBUG` re-export) when adding new networked subcommands.

None of these apply to `lakitu` (server-side, runs on the circuit, not on Android).
