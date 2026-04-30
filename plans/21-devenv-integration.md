# Plan 21 — devenv as a first-class kart runtime alongside devcontainers

## Why

Today every kart is a docker container managed by devpod from a `devcontainer.json`. That gives drift uniform isolation and a portable image story, but it also pays docker's full price on every host: a writable layer per kart, image pulls / builds, an extra IP namespace, a UID-mapping dance, and a 200–500 MB Nix install layer baked into the image whenever a tune sets `flake_uri`. On a NixOS lakitu host that already has `nix` and an unprivileged user account, almost all of that cost is redundant — the host can hand the kart a real shell with the right tools on PATH directly, with no container in the loop.

[devenv.sh](https://devenv.sh/) is the natural fit for that "no container, all Nix" path. It composes:

- A flake-pinned developer environment (`devenv.nix`, `devenv.yaml`) — same content-addressed inputs the existing `flake_uri` story already uses.
- A native process manager (process-compose) that owns service lifecycle, with declarative ports (`ports.<n>.allocate`), strict-mode conflict reporting (`devenv up --strict-ports`), and systemd-style socket activation. See research notes from this branch's discussion.
- A `devenv shell` entrypoint that drops the user into the populated environment without any container boundary.

The shape we want for drift: **the kart's runtime is a *backend* — devcontainer or devenv — picked per-tune and per-host, with the rest of drift (kart lifecycle, characters, seeds, ports.yaml, ssh aliases, dotfiles, chest, deny-literals, claude-code seed) behaving identically across both**. Operators on a NixOS lakitu get a faster, lighter path; operators on a non-Nix lakitu (or for tunes that genuinely need OS-level isolation) keep devcontainers. A single circuit can run both at once.

The `flake_uri` plumbing already added by plan 17 is the right starting point: it proves drift can drive a Nix-flavoured kart, just one wrapped in a container today. devenv collapses the wrapper.

## Goals

1. **Two runtimes, one drift.** A kart created from a devenv tune behaves the same to the user as a kart created from a devcontainer tune: `drift new`, `drift connect`, `drift kart info`, `drift ports`, `drift kart restart`, `drift kart delete` all work without runtime-aware flags.
2. **Per-tune runtime declaration.** A tune declares which runtime(s) it supports. `runtime: devcontainer` and `runtime: devenv` are exclusive; `runtime: either` advertises both with parallel config. The resolver picks one at `kart.new` time based on host capabilities + operator override.
3. **Native execution on Nix hosts.** When devenv is on PATH on the lakitu host and the resolved tune supports it, drift runs the kart natively with no docker dependency. devcontainer-only tunes still work.
4. **Feature parity.** The drift-managed surface (characters / dotfiles / seeds / chest env / `flake_uri` / nix-cache / deny-literals / `drift ports` / claude-code seed / `info.json`) lands the same in a devenv kart as in a devcontainer kart, including the in-kart zellij topbar, gh PAT injection, and per-kart $HOME.
5. **Drift "features" map to nix configs for devenv.** The feature set currently expressed as a devcontainer features JSON map gets a parallel expression as devenv module fragments. Drift composes the two from the same registry so a tune author writes one feature list and gets both renderings.
6. **Transparent, no leakage.** A user reading `drift kart info` sees `runtime: devenv` (or `runtime: devcontainer`) but is otherwise unaware of the backend; SSH alias remains `drift.<circuit>.<kart>`; `~/.drift/info.json` is identical.

## Non-goals

- **Cross-runtime kart migration.** A devcontainer kart and a devenv kart are separate kart records. We do not migrate one into the other in v1; if a user wants to switch runtimes for an existing kart they create a new one and copy state. (Migration could come later — slot under plan 09's machinery — but the surface is wider than this plan should own.)
- **devenv on non-Nix lakitus.** devenv requires nix; if the host doesn't have nix, the runtime is unavailable. We do not ship a nix bootstrap as part of drift. The detection is "is `devenv` on PATH" + "is `nix` on PATH"; absent either, devenv tunes fail with a clear error.
- **OS-level isolation for devenv karts.** A devenv kart shares the lakitu user's filesystem and process namespace by default. Per-kart users (a `kart-<name>` system account with home isolation) are out of scope for v1 — they re-introduce most of the complexity docker takes off our plate. Operators who need that level of isolation use the devcontainer runtime.
- **Auto-translation of arbitrary devcontainer features into devenv.** We curate the bridging set (the features drift itself injects: nix cache, github-cli, claude-code seed deps). Third-party devcontainer features remain devcontainer-only.
- **process-compose UI integration.** devenv's `devenv up` runs process-compose; the user can `drift connect` to a kart and run it themselves. Drift does not auto-start services on `kart.start` for v1.
- **Workstation-side install.** drift the client doesn't grow a devenv dependency. All devenv work happens on lakitu.

## Architecture

### Runtime as a backend abstraction

Today `internal/kart` calls `internal/devpod` directly. The new shape factors that into a small `Runtime` interface in `internal/kart/runtime/` (or similar):

```go
type Runtime interface {
    Name() string                                            // "devcontainer" | "devenv"
    Available(ctx context.Context) error                     // host capability probe
    Up(ctx context.Context, opts UpOpts) (*UpResult, error)  // create/start
    Down(ctx context.Context, name string) error             // stop
    Delete(ctx context.Context, name string) error           // remove
    SSH(ctx context.Context, opts SSHOpts) error             // interactive shell
    SSHRun(ctx context.Context, opts SSHRunOpts) (Out, error) // non-interactive command
    ProbePorts(ctx context.Context, name string) ([]Port, error) // for `drift ports auto`
    Info(ctx context.Context, name string) (*RuntimeInfo, error) // status, image/flake, mounts
}
```

The devcontainer implementation wraps the existing `internal/devpod` client. The devenv implementation lives in a new `internal/devenv/` package and shells out to `devenv`, `nix`, and `process-compose` directly. `kart.New` (and `kart.start` / `kart.stop` / `kart.delete`) pick a `Runtime` from a small registry indexed by name; everything above the runtime call site (resolver, seeds, info.json render, character injection, ports.yaml, claude-status, deny-literals) stays runtime-agnostic.

The interface is intentionally narrow: anywhere the existing code reaches into devpod for something that's *runtime-specific* (workspace UID, mount-source path translation, `--recreate`, devpod context options, `DEVPOD_HOME`), we either move it behind the interface or scope it to the devcontainer implementation. The tell for what should be on the interface: code that runs on every kart regardless of backend goes through the interface; code that only makes sense for one backend stays in that backend's package.

### Host-capability probe and runtime selection

On lakitu startup (or lazily on first kart.new), the server probes:

- `nix --version` → nix-capable host
- `devenv version` → devenv-capable host (implies nix-capable)
- `docker info` → docker-capable host
- `devpod version` → devpod-capable host (implies docker-capable)

Capabilities are cached for the server's lifetime (with a single re-probe on SIGHUP). The `Resolver` consults them when picking a runtime for a kart.

Selection rule (single, predictable):

1. If `--runtime=<name>` is passed to `drift new`: use that one. Error if unsupported by the resolved tune or unavailable on the host.
2. Else if the tune declares a single runtime: use that. Error if unavailable on the host.
3. Else if the tune declares `runtime: either`: prefer `devenv` when both are available, fall back to `devcontainer`. Operators who want the inverse pin via `default_runtime` on the server config (`~/.drift/garage/config.yaml`).
4. Else if neither runtime fits: error with a message naming the tune's declared runtimes and the host's available ones.

The choice is recorded on the kart's `config.yaml` as `runtime: <name>`. Subsequent `kart.start` / `kart.delete` / `drift connect` honour that record without re-evaluating; a kart's runtime is fixed at creation. A future `drift kart migrate-runtime` could shift it, but as noted in non-goals, not for v1.

### Tune schema additions

`internal/model/types.go::Tune` grows a small set of fields. None are required for existing devcontainer tunes — the resolver treats their absence as "devcontainer only," matching today's behaviour:

```go
type Tune struct {
    // ... existing fields ...

    // Runtime declares which kart runtime(s) this tune supports. One of
    // "devcontainer" (default if omitted, matches v0 behaviour),
    // "devenv", or "either". When "either", `Devenv` and the existing
    // devcontainer fields must both be populated.
    Runtime string `yaml:"runtime,omitempty" json:"runtime,omitempty"`

    // Devenv carries the devenv-runtime-specific configuration. Required
    // when Runtime is "devenv" or "either"; ignored otherwise. Mirrors
    // the shape of devenv.nix / devenv.yaml so a tune author can think
    // in devenv terms directly.
    Devenv *TuneDevenv `yaml:"devenv,omitempty" json:"devenv,omitempty"`
}

type TuneDevenv struct {
    // FlakeURI is the kart's primary devenv flake — usually
    // github:<org>/<repo>?dir=path-to-devenv. Drift renders a thin
    // `devenv.yaml` pointing at it as the kart's `inputs.<name>` and
    // imports the matching modules into the kart's `devenv.nix`. May be
    // omitted when the only configuration is in `Modules` below.
    FlakeURI string `yaml:"flake_uri,omitempty" json:"flake_uri,omitempty"`

    // Modules is a list of devenv module fragments — Nix snippets that
    // get composed into the kart's `devenv.nix` `imports` list (when each
    // is a flake reference) or inlined as a Nix file (when a literal
    // expression). Parallel to the devcontainer `features` map. See
    // "Drift features as nix module fragments" below.
    Modules []string `yaml:"modules,omitempty" json:"modules,omitempty"`

    // ExtraConfig is a free-form Nix expression spliced into `devenv.nix`
    // verbatim. The escape hatch for tune authors who want to tune
    // process-compose / language settings / ports / services without
    // factoring them into a module. Format: a Nix attrset body without
    // the wrapping braces (`languages.go.enable = true;` etc).
    ExtraConfig string `yaml:"extra_config,omitempty" json:"extra_config,omitempty"`

    // Ports declares the user-facing ports drift should track in
    // ports.yaml automatically when this tune is used. Each entry is
    // a base port; devenv's `ports.<n>.allocate` resolves the actual
    // listen port. Drift records both (`requested` + `allocated`) on
    // the kart record so `drift ports` can render them clearly.
    Ports []TuneDevenvPort `yaml:"ports,omitempty" json:"ports,omitempty"`
}

type TuneDevenvPort struct {
    Name     string `yaml:"name" json:"name"`             // e.g. "http"
    Allocate int    `yaml:"allocate" json:"allocate"`     // base port; auto-shifts on conflict
    Strict   bool   `yaml:"strict,omitempty" json:"strict,omitempty"` // fail instead of shifting
}
```

For the `Runtime: either` case, the resolver enforces both halves are populated and consistent at tune-load time so the failure surfaces at `tune save`, not at `kart.new`.

### Drift features as nix module fragments

Drift currently injects three devcontainer features automatically depending on context (plan 17 + plan 19 + plan 20):

- `ghcr.io/devcontainer-community/devcontainer-features/nixos.org:1` — when `flake_uri` is set
- `ghcr.io/devcontainers/features/github-cli:1` — when the resolved character has a PAT
- `ghcr.io/devcontainers/features/nix:1` (legacy) — historically; nixos.org community feature now preferred

For devenv karts, the parallel pieces are:

| Drift concern | Devcontainer (today) | Devenv (this plan) |
|---|---|---|
| Provide nix | nix feature install layer | host already has nix |
| Substituters | `extraNixConfig` injected into nix feature | `nix.substituters` / `nix.trusted-public-keys` written into `devenv.nix` (or via `~/.config/nix/nix.conf` overlay if devenv supports a per-env nix.conf — phase 1 to confirm) |
| `flake_uri` install | postCreateCommand: `nix profile install <uri>` | `devenv.yaml` `inputs.<name>` + `devenv.nix` import or `nix profile install` from a postShell hook (phase 1 to confirm which behaves better when the kart is recreated) |
| github-cli when PAT present | `github-cli:1` feature | `packages = [ pkgs.gh ];` module |
| Claude code seed prereqs | seed drops files into container `$HOME` | seed drops files into `<kart-home>/` (the lakitu-side per-kart $HOME — see "Filesystem layout") |

Drift maintains a small registry mapping each "drift-managed feature" to both renderings:

```go
// internal/kart/featureregistry/registry.go (sketch)
type Feature struct {
    Name           string                 // "nix-cache", "github-cli", "claude-code"
    Devcontainer   func(ctx FeatureCtx) (id string, opts map[string]any)
    DevenvModule   func(ctx FeatureCtx) (nixSnippet string)
}
```

The resolver walks the registry against the kart's resolved context (PAT presence, NixCache info, runtime choice) and emits whichever rendering applies. A devcontainer kart sees the `Devcontainer` outputs spliced into the existing features JSON; a devenv kart sees the `DevenvModule` outputs spliced into the generated `devenv.nix`.

Tune-author features (the `features:` JSON or `devenv.modules:` list) compose on top — same resolver merge order: server defaults → tune → flag overrides → drift auto-injections. The auto-injection for a devenv kart is *additive on devenv.nix*, mirroring how the devcontainer auto-injection is additive on the features map.

### Filesystem layout for a devenv kart

A devenv kart lives in two directories on the lakitu host:

1. **The kart's project directory** — where the cloned/started source lives. By default `~/.drift/garage/karts/<name>/work/` (sibling of the `config.yaml` already written for devcontainer karts). The user's `cwd` when they `drift connect`.
2. **The kart's home overlay** — `~/.drift/garage/karts/<name>/home/`. Drift treats this as the kart's `$HOME`. Seed files (`info.json`, `claudeCode` drop, dotfiles) land here. Set as `HOME=...` in the entrypoint that wraps `devenv shell`.

`devenv.nix` and `devenv.yaml` are rendered into the project dir at `kart.new` time. `devenv.lock` lives there too — committed to the kart's project tree if the user pushes, otherwise just persisted on disk like any other kart state.

Mounts (`tune.mount_dirs`) for a devenv kart resolve to **symlinks**, not docker binds: a `mount_dir` with target `~/.config/foo` becomes `<kart-home>/.config/foo -> <source-path>`. Same `~/`-on-both-sides spec the user already writes; the source side resolves against lakitu's actual `$HOME`.

### SSH path

The existing ssh alias `drift.<circuit>.<kart>` is wired up by drift's workstation client to land the user inside the kart. For devcontainer karts, that resolves through `lakitu` to a `devpod ssh <kart>` invocation that exec's into the container.

For devenv karts, the destination is the *lakitu user account* with a `RemoteCommand` that:

1. `cd <kart-project-dir>`
2. exec's `devenv shell -- env HOME=<kart-home> $SHELL`

Concretely, lakitu's existing ssh-config seed (the one that lays down `Host drift.<c>.<k>` blocks) grows a runtime-aware `RemoteCommand`: same alias, different one-line wrapper. The workstation-side `drift connect` doesn't change at all — it ssh's to the alias, the right thing happens.

`devenv shell` is a long-lived process that holds the populated env; the user's interactive shell runs underneath it. Closing the shell drops the user out of the kart cleanly. Reconnecting re-enters a fresh `devenv shell`; nix's content-addressed store makes that effectively instant on warm caches.

For the non-interactive `kart.SSHRun` paths (seed application, info.json render, port probes), drift uses `devenv shell --` followed by the command. Same wrapper, no TTY, captured stdout.

### Ports

`drift ports` (plan 13) becomes simpler for devenv karts in some ways and unchanged in others.

- Workstation-side: identical. The user's `~/.config/drift/ports.yaml` still maps a workstation port to a "kart port"; the ssh ControlMaster is still per-kart.
- Lakitu-side port discovery: instead of `ss -tlnH` inside a container, drift queries the kart's running process-compose for its allocated ports (process-compose has a stable `--port-info` / API endpoint we can hit, or we read the rendered devenv `ports.<n>.value` straight from `devenv print`). The result feeds `kart.probe_ports` exactly like today.
- Multi-kart conflict on lakitu: this is a real concern for devenv karts. Two karts both declaring `ports.http.allocate = 3000` will land on `:3000` and `:3001` respectively via devenv's auto-shift. Drift records the *allocated* port (not the requested one) on `kart.start` and surfaces it through `kart.probe_ports`, so the workstation forward chain points at the right place automatically. `tune.devenv.ports[].strict = true` opts a tune into devenv's strict mode so a port collision fails loudly instead of silently shifting.

### Lifecycle

Per-runtime mapping of the existing kart-level lifecycle verbs:

| Verb | Devcontainer | Devenv |
|---|---|---|
| `kart.new` | `devpod up` + finalisers | render `devenv.nix` + `devenv.yaml`, `devenv up --detach`, run finalisers (seeds, dotfiles install, info.json) |
| `kart.start` | `devpod up` (warm) | `devenv up --detach` |
| `kart.stop` | `devpod stop` | `devenv processes stop` |
| `kart.restart` | `devpod stop` + `devpod up` | `devenv processes stop` + `devenv up --detach` |
| `kart.delete` | `devpod delete` | `devenv processes stop`, remove `~/.drift/garage/karts/<name>/{work,home}` |
| `kart.list` | `devpod list` cross-referenced against garage | walk garage (devenv has no separate state we'd reconcile against) |

`kart.delete` for a devenv kart is *much* more obviously destructive than for a devcontainer kart — there is no separate writable layer to remove, the user's project work *is* the project dir. Drift refuses to delete a devenv kart whose project dir has uncommitted/unpushed git state without a `--force`, mirroring the existing devcontainer warnings around `--recreate` (per the `feedback_no_destructive_container_ops` memory).

### Seeds and characters

Seeds (`internal/seed`) are runtime-agnostic as written: they emit `RenderedFile{Path, Content, OnConflict, BreakSymlinks}` and the kart finaliser turns those into shell that runs against the kart's `$HOME`. The runtime abstraction's `SSHRun` is the place where "how do I land bytes in this kart's $HOME" is implemented — devcontainer runs it through `devpod ssh --command`, devenv runs it by writing directly to `<kart-home>/<path>` on the lakitu filesystem. Seed code never has to know the difference.

Characters: same. The character's git/SSH/PAT material flows into seeds (gitconfig, gh hosts) and into the chest-resolved env (which becomes `process.env` for devenv-launched processes via the same `--workspace-env` style hook devenv exposes). The PAT-injection shortcut that currently splices the github-cli devcontainer feature flips to the registry's `gh-cli` devenv module for devenv karts.

The `claudeCode` seed (which carries the deny-literals hook from plan 20) drops the same files into `<kart-home>/.claude/`. Hook still installs, deny-literals still applies.

### info.json and the in-kart UI

`~/.drift/info.json` (read by the zellij topbar prelude in `features/nixenv`) is identical in shape across runtimes: `{kart, character, circuit, timezone}`. Drift writes it to `<kart-home>/.drift/info.json` for devenv karts and to the in-container `$HOME/.drift/info.json` for devcontainer karts. The bashrc prelude in the nixenv feature already reads from `$HOME/.drift/info.json` and renders zellij's `identity.json`, so devenv karts pick up the same chrome with no further work. The drift-devtools profile (`drift-devtools` from `features/nixenv`) installs into the kart's `nix profile` either way.

### Drift / lakitu CLI surface

- `lakitu kart show <name>` adds a `Runtime: devenv` (or `devcontainer`) line near the top.
- `lakitu kart show <name>` for a devenv kart prints the resolved `devenv.nix` path and the merged module list.
- `drift new` accepts `--runtime=<devcontainer|devenv>` as an explicit override (must be supported by the chosen tune; `--tune=foo --runtime=devenv` errors if foo is `runtime: devcontainer`).
- `lakitu` config gains a `default_runtime` field (`devcontainer` for backwards compat; operators on Nix lakitus can flip it to `devenv`).
- `drift kart info` surfaces the runtime in the JSON output (`"runtime": "devenv"`) so workstation-side TUIs (plan 14) can render it as a small badge.

No new top-level subcommand — the runtime is a property of the kart, surfaced via the existing kart info / show paths.

## Phasing

Each phase ends in a working state on `main` with neither runtime broken.

### Phase 1 — runtime abstraction, devcontainer-only

Refactor only. No devenv code yet.

- Introduce `internal/kart/runtime.Runtime` interface.
- Move existing devpod-driven `kart.New` logic behind a `runtime.Devcontainer` implementation with no behaviour change.
- Wire the resolver to ask the runtime for capabilities (probe results), even though there's only one runtime to choose from.
- Persist `runtime: devcontainer` on `KartConfig` for every newly created kart. Existing karts without the field continue to work — absence is treated as `devcontainer`.
- `lakitu kart show` displays the runtime line (always `devcontainer` after phase 1).

This is the load-bearing refactor; until it lands, every later phase has to keep editing the same call sites.

### Phase 2 — devenv runtime, no auto-features

Land the `internal/devenv` package and a `runtime.Devenv` implementation that can:

- `Up`: render `devenv.nix` + `devenv.yaml` from a tune, run `devenv up --detach`.
- `SSH` / `SSHRun`: wrap `devenv shell -- ...`.
- `Down` / `Delete`.
- `ProbePorts`: read `devenv print` for resolved port values.

Tune schema additions (`Runtime`, `TuneDevenv`) + `--runtime` flag + capability probe.

A tune author can now write a minimal devenv tune by hand (`runtime: devenv`, `devenv.flake_uri: ...`, optional `devenv.extra_config`). drift creates the kart, the user `drift connect`s, and lands in a `devenv shell`. No drift-managed features are auto-injected yet — the tune is responsible for everything.

### Phase 3 — feature registry + auto-injection

Introduce the `featureregistry` with the three drift-managed features (`nix-cache`, `github-cli`, `claude-code`), each carrying both a `Devcontainer` and `DevenvModule` rendering. Move the existing devcontainer-feature auto-injection in `internal/kart/flags.go` to use the registry (no behaviour change). Then turn on the parallel devenv-side injection so a devenv kart gets the same trio when the conditions match (PAT present → gh module; circuit cache configured → substituters wiring; etc).

### Phase 4 — seeds, dotfiles, characters, info.json

Make every drift-managed surface that today writes into a container's `$HOME` write into `<kart-home>/` for devenv karts via the runtime abstraction. Confirm the nixenv flake's bashrc prelude renders zellij `identity.json` correctly under devenv. Pass an integration test that creates a devenv kart from a fixture tune, executes a seeded `claude` deny-literals attempt, and confirms the hook fires.

### Phase 5 — ports.yaml integration + multi-kart on one host

Hook `kart.probe_ports` for devenv karts into `devenv print` / process-compose. Add the `tune.devenv.ports` field. Run two devenv karts in parallel with overlapping `allocate` bases, confirm auto-shift gives each its own resolved port and `drift ports` forwards reach the right one.

### Phase 6 — operator polish

- `default_runtime` server config field.
- `lakitu kart show` runtime-aware rendering (resolved devenv.nix path, module list).
- Drift kart `--force` confirmation flow for delete-with-uncommitted-state (devenv only — for devcontainer the writable layer already gates this).
- Documentation pass: README section, `lakitu kart new --help`, an example tune in `examples/devenv/`.

## Open questions

These are the live design questions. Phase 1 mostly clarifies them by forcing the abstraction; phase 2 confirms.

1. **Per-env `nix.conf` for substituters under devenv.** devenv runs against the host nix daemon; cache substituters configured on the host's `nix.conf` are already in scope for any `devenv shell` invocation. If the lakitu host has the circuit's harmonia substituter wired into its system `nix.conf` (which plan 17 leaves to the operator), no per-kart injection is needed. If not, we either inject into `devenv.nix` (`nix.substituters = ...`) or write a per-kart `~/.config/nix/nix.conf` overlay in the entrypoint. Phase 1 should confirm devenv's preferred path.
2. **`devenv up` vs `devenv shell` on `kart.start`.** devenv's `up` starts long-running services; many karts won't have any (they're shell-only). Probably: `kart.start` runs `devenv up --detach` only when `processes` are declared in the rendered `devenv.nix`, else just touches the lock to prime caches.
3. **Workstation-on-Termux talking to a lakitu running devenv karts.** Should be transparent — the only thing the workstation sees is the SSH alias — but this needs explicit testing because the `drift connect` mosh fallback (plan 13) interacts with the wrapping `devenv shell` differently from a docker-exec'd shell. Notably `RemoteCommand` interaction with mosh-server bootstrap.
4. **Per-kart users.** This plan punts on per-kart system users for v1. The right time to revisit is after phase 5: if operators are running 5+ devenv karts on one lakitu and start tripping over each other's `~/.config/...`, a `kart-<name>` system account becomes worth the wiring complexity. For v1, the kart-home overlay (under `~/.drift/garage/karts/<name>/home/`) is the isolation primitive.
5. **`flake.lock` discipline for devenv karts.** Drift's own `flake.lock` discipline (CLAUDE.md) is about the drift repo. The kart's own `devenv.lock` is per-kart state; we don't auto-bump it on `kart.start`. Operators run `devenv update` inside the kart when they want a refresh; the lock is part of the kart's project dir and tracks however the user prefers.
6. **`ExtraConfig` injection format.** Splicing a free-form Nix snippet into the rendered `devenv.nix` is an easy footgun if the snippet redefines an attribute the registry already set. We probably want a deterministic precedence (`registry < tune.devenv.modules < tune.devenv.extra_config`) and a smoke-test that catches the obvious overlap cases. Phase 3 to nail down.

## Risk and rollback

The runtime abstraction (phase 1) is the only refactor with broad blast radius. If it lands and devcontainer karts regress, revert is one PR. Phases 2–6 are additive: every devenv-specific code path is gated on `runtime: devenv`, and an operator's existing devcontainer karts are unaffected by anything past phase 1 unless they explicitly opt in.

devenv itself is a Nix-flavoured dependency on the lakitu host. If a circuit operator doesn't want the dependency, they don't enable it; capability probing handles the rest. The plan does not change anything about how a non-Nix lakitu runs.
