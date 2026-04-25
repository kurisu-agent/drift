# STOP — before your first code edit

Planning, reading, grepping, and running read-only commands in the main checkout is fine. You only need a feature worktree once you're about to make an edit that affects the codebase — anything CI runs (Go source, tests, `integration/`, `flake.nix`, `Makefile`, `.github/workflows/`, `packaging/nix/`).

Doc-only edits stay on main: `*.md` files (`CLAUDE.md`, `README`, `plans/**`), `.gitignore`, `TODO.md`, comments in `docs/`. If the edit can't change a build, test, or integration outcome, commit it directly.

**When you are about to Edit or Write a code file, first check where you are:**

```
git rev-parse --show-toplevel
```

If that prints the repo root (the main checkout), stop and set up a worktree before the edit. From the repo root:

```
git worktree add .claude/worktrees/<feature> -b feat/<feature> main
cd .claude/worktrees/<feature>
```

The path printed by `git rev-parse --show-toplevel` should now end in `.claude/worktrees/<feature>`.

## If you realize you're on main mid-edit

Don't `git restore` to discard your work. Move the diff instead. From the main checkout:

```
git stash push -m "<feature>" -- <paths you touched>
git worktree add .claude/worktrees/<feature> -b feat/<feature> main
cd .claude/worktrees/<feature> && git stash pop
```

Continue from there.

# Feature workflow

Direct pushes to `main` are reserved for trivial changes (typos, plan-doc tweaks, `.gitignore`, README edits — things that can't plausibly affect integration). Anything else goes through a feature branch and a pull request.

**Use a git worktree per feature.** Keep the `main` checkout clean for reviewing / bisecting / cherry-picking, and let each in-flight feature live in its own directory:

```
git worktree add .claude/worktrees/<feature> -b feat/<feature> main
```

Worktrees live under `.claude/worktrees/` (already gitignored). One feature per worktree, one branch per feature. When the feature lands, `git worktree remove` to reclaim the directory.

**Enable the pre-commit hook once per clone** with `make install-hooks` (sets `core.hooksPath = .githooks`, so every worktree inherits it). The hook runs `gofmt -w` and the full `golangci-lint run --fix` on staged Go packages, re-stages anything it rewrites, and fails only when issues remain after auto-fix. Full lint parity with CI is deliberate — an earlier `--fast-only` variant was faster but let errorlint-class issues sail through to CI and cost a round trip per miss.

**Run `make ci` before every code push.** The pre-commit hook only runs gofmt + golangci-lint on staged packages; `make ci` expands to `tidy vet test-race lint vuln` — the full CI-parity suite. It catches race-detector bugs, `govulncheck` flags, and `go mod tidy` churn that the hook can't see; each miss is a round trip to GitHub CI. `make ci` auto-reenters `nix develop` when called from a bare shell so the flake-pinned `golangci-lint` / `govulncheck` / `gcc` (for `-race`) are on PATH — no prefix required, one command from anywhere. Also run `make integration` when touching anything SSH/devpod/transport-shaped: ~2-3 min with docker, gated behind the `integration` build tag, so `make ci` deliberately skips it.

`make ci` and `make integration` both blow past the default 2-minute Bash timeout in agent harnesses. The cliscript suite alone is ~9 min under `-race`; integration is ~3 min including docker. Always run these with `run_in_background: true` (and read the output file when notified) or set an explicit 10-minute timeout — never rely on the default. Per-package fast iteration (`go test ./internal/seed/...` etc.) stays well under the default and is the right tool when you're not gating a push.

Doc-only pushes skip `make ci`: if the diff is all `*.md` / `plans/**` / `TODO.md` / `.gitignore` / comments under `docs/` — nothing that feeds into `go build` / `go test` / `golangci-lint` / `govulncheck` — push directly. The local `make ci` exists to pre-empt GitHub-CI round trips on code changes; for docs the round trip doesn't exist (CI doesn't lint markdown). If you're unsure whether an edit is code-affecting, run it.

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

If a downstream consumer wires drift into a NixOS flake via a path input (`path:/path/to/drift`), `nixos-rebuild` satisfies that input from the consumer's `flake.lock` `narHash`, so new drift commits stay invisible until the lock is bumped — even with `--impure`. Before tagging, refresh and test on any such consumer so the flake.lock commit records the exact drift state the tag corresponds to:

```
nix flake update drift --flake /path/to/consumer
nixos-rebuild switch --flake /path/to/consumer#<hostname>
```

Verify with `grep -ao '<some-new-string>' "$(which lakitu)"` — the rebuilt binary should contain any new flag or identifier added by the release.

# Backwards compatibility

Drift is pre-1.0 (currently v0.n). Don't write backwards-compat shims, migration paths, or deprecation stubs for internal shape changes — tune fields, kart config keys, RPC payloads, on-disk garage layout, flag names. Change the shape, update the callers, move on. No `omitempty` gymnastics to preserve old files, no "if field is missing, fall back to old behaviour" branches, no renames that keep the old name as an alias. The only exception is user-facing CLI surface that people have muscle memory for (`drift new`, `drift connect`), and even then only when the user explicitly flags it.

Revisit this rule when tagging v1.0.

# Client / server boundary — keep `drift` thin

When designing a new feature, default to putting the logic on **lakitu** (server, on the circuit). Reach for `drift` (workstation client) only for the parts that *fundamentally* have to live on the user's box: rendering UI, reading workstation-local state (config files, ports.yaml, ~/.ssh/config), opening interactive sessions, and binding workstation-side ports.

Why: lakitu already speaks devpod, owns `DEVPOD_HOME`, knows each kart's container user / image / mounts, has access to the chest, and runs as a known account on a known OS. Replicating any of that on the workstation means reimplementing it across macOS / Linux / Termux, plus negotiating the wildcard `Host drift.<c>.<k>` ssh alias (which has its own user-mapping headaches per upstream image) instead of just calling an RPC. A `lakitu` change ships in one binary; a `drift` change ships in three.

The repeated tell: *"to do X, drift would need to ssh into the kart and …"*. Stop. Add a `kart.X` RPC instead. The server already knows how to reach the kart; the client just calls it. `kart.probe_ports` is the canonical example — `ss -tlnH` runs server-side, the client only renders the picker and writes ports.yaml.

Counter-cases (legitimately client-side):
- The interactive shell of `drift connect` — has to attach to the user's terminal.
- The actual workstation listener (`ssh -L`, `-O forward`) — only the workstation can bind a workstation port.
- ports.yaml itself — per-workstation state by design (per plan 13).
- ssh_config writes — the user's local ssh setup.

Everything else: lakitu.

# Building drift binaries by hand

Plain `go build ./cmd/drift` inside `nix develop` produces a binary dynamically linked against the nix-store glibc (`/nix/store/.../ld-linux-x86-64.so.2`). That works on this dev VM, but `drift update <devvm>:/path/to/binary` ships it to a different host whose loader path doesn't exist, and the kernel rejects it as `command not found` even after `chmod +x`. Goreleaser builds with `CGO_ENABLED=0` for exactly this reason; ad-hoc hand-builds need the same flag:

```
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o bin-drift ./cmd/drift
```

`file bin-drift` should report `statically linked` and resolve to ~12 MiB stripped. If it says `dynamically linked, interpreter /nix/store/...`, the binary won't run anywhere except the build host. This applies to ad-hoc `drift update` source binaries; CI / release tarballs are unaffected because goreleaser already pins `CGO_ENABLED=0`.

# External repo references

Never reference other repositories, organisations, or user handles in anything that lands in this repo — commits, code, docs, plans, commit messages, tests, examples. Only this repo (`kurisu-agent/drift`) and its dependencies may appear. Unless the user explicitly requires it in the current turn, use generic placeholders (`example-org`, `<your-org>`, etc.) in examples and documentation.

# Markdown style

Don't hard-wrap markdown. Write each paragraph as a single long line and let the editor or viewer soft-wrap it. Hard-wrapped markdown produces noisy diffs when a single word changes (every line below the edit reflows), is awkward to edit (the wrap point is wrong as soon as you add or remove text), and is invisible at render time anyway. This applies to every markdown file in the repo: `CLAUDE.md`, `README`, `plans/**`, `TODO.md`, anything under `docs/`. Lists, code blocks, and tables follow normal markdown rules; the no-hard-wrap rule is only about prose paragraphs.

# Termux/Android is a supported `drift` target

The `drift` client runs on Termux (Android) as a first-class platform — release tarballs ship `drift_<ver>_android_arm64.tar.gz`. When touching `drift` CLI code, assume the binary may be running on Termux and watch for these traps:

- **`os.Executable()` lies.** termux-exec runs every $PREFIX binary through the Android dynamic linker to bypass W^X SELinux. That makes `/proc/self/exe` (and thus `os.Executable()`) resolve to `/apex/com.android.runtime/bin/linker64` — not drift. Code that needs the running binary's real path must fall back to argv[0], which the linker preserves. See `resolveSelfPath` in `internal/cli/drift/update.go`.
- **`/apex` and `/system` are read-only.** Any write path derived from `os.Executable()` will hit EROFS on Android. Always anchor writes to `$HOME`, `$PREFIX`, or an explicit user-supplied path.
- **`exec` needs the linker wrap.** Don't call `os/exec` directly for binaries under `$PREFIX`; go through `internal/exec`, which handles the W^X escape hatch (`termuxLinkerWrap`).
- **No `/etc/resolv.conf`.** Go's pure-Go resolver fails when it's missing. Preserve the fallback wired up in `internal/cli/drift/dnsfix.go` (and the `DRIFT_DEBUG` re-export) when adding new networked subcommands.

None of these apply to `lakitu` (server-side, runs on the circuit, not on Android).

# Invoking `devpod` manually on a circuit

lakitu stores devpod state under `~/.drift/devpod/`, not the default `~/.devpod/`. Running `devpod list` / `devpod ssh <kart>` / `devpod delete <kart>` against lakitu's state requires `DEVPOD_HOME=~/.drift/devpod`:

```
DEVPOD_HOME=~/.drift/devpod ~/.drift/bin/devpod list
DEVPOD_HOME=~/.drift/devpod ~/.drift/bin/devpod ssh <kart> --command '…'
```

Bare `devpod list` will show no workspaces even when lakitu has several. When a failed kart.new leaves garage state that `lakitu` won't clean up, `DEVPOD_HOME=~/.drift/devpod ~/.drift/bin/devpod delete <name> --force` is the escape hatch before `rm -rf ~/.drift/garage/karts/<name>`.
