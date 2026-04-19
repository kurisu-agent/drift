# drift — implementation plan

## Overview

drift is a Go project — two compiled binaries distributed independently. It wraps [devpod](https://github.com/skevetter/devpod) (maintained fork) to add specific DX quality-of-life features:

- **Server-side management** — workspaces live entirely on remote circuits; clients are stateless
- **SSH-native transport** — every client↔server call is JSON-RPC 2.0 over a plain SSH channel. No custom daemon, no listening ports, no bespoke auth. Whatever lets you `ssh` to the circuit is what drift uses
- **Identity and secrets** — first-class git identity profiles (characters) and pluggable secret storage (chest)
- **Persistent connections** — mosh-based terminal sessions that survive network changes
- **Tune profiles** — reusable presets composing devcontainer features, starters, and dotfiles
- **Multi-client** — any workstation can connect to any circuit without syncing state

devpod itself is only ever invoked by `lakitu` on the circuit. The `drift` client has no devpod dependency.

---

Two binaries, shared wire types, same handlers on both I/O paths:

- **`drift`** — client CLI on workstations. Zero local state. Speaks JSON-RPC 2.0 to lakitu over SSH.
- **`lakitu`** — server-side binary on each circuit. Owns all state. Invoked per-call as `lakitu rpc` (by drift, short-lived) or via named subcommands (by humans administering a circuit directly). Both paths dispatch to the same Go handlers.

Client and server versions are matched via **semver**. See [Version compatibility](#version-compatibility).

---

## Architecture

```
  workstation-1          workstation-2          workstation-3
  ┌───────────┐          ┌───────────┐          ┌───────────┐
  │   drift   │          │   drift   │          │   drift   │
  │           │          │           │          │           │
  │ ~/.config │          │ ~/.config │          │ ~/.config │
  │  /drift/  │          │  /drift/  │          │  /drift/  │
  │  config   │          │  config   │          │  config   │
  └─────┬─────┘          └─────┬─────┘          └─────┬─────┘
        │                      │                       │
        │ JSON-RPC/SSH + mosh  │                       │
        ├──────────────────────┤           ┌───────────┘
        │                      │           │
        ▼                      ▼           ▼
┌───────────────────┐   ┌───────────────────────────────────┐
│    circuit-1      │   │            circuit-2              │
│                   │   │                                   │
│  lakitu           │   │  lakitu                           │
│  ~/.drift/garage/ │   │  ~/.drift/garage/                 │
│                   │   │                                   │
│  ┌─────────────┐  │   │  ┌─────────────┐ ┌─────────────┐ │
│  │    kart     │  │   │  │    kart     │ │    kart     │ │
│  │   proj-a    │  │   │  │   proj-b    │ │   proj-c    │ │
│  │  (running)  │  │   │  │  (running)  │ │  (stopped)  │ │
│  │             │  │   │  │             │ │             │ │
│  │ devcontainer│  │   │  │ devcontainer│ │ devcontainer│ │
│  └─────────────┘  │   │  └─────────────┘ └─────────────┘ │
│                   │   │                                   │
│  docker           │   │  docker                           │
└───────────────────┘   └───────────────────────────────────┘
```

**Key properties:**
- Workstations hold zero state — only circuit config (`~/.config/drift/config`)
- Any workstation can connect to any circuit
- Multiple workstations can target the same circuit simultaneously
- All kart state, characters, tunes, and chest entries live in the circuit's garage
- **State is per-circuit.** Characters, chest entries, and tunes are NOT synced across circuits. Cross-circuit sync (plugin-driven, syncthing, git-backed) is deferred — see [Future](#future).
- **Every drift command is one JSON-RPC call** over a fresh SSH invocation (`ssh <circuit> lakitu rpc`). lakitu isn't a long-running daemon; it runs, answers, exits. OpenSSH `ControlMaster` can amortize TCP setup for users who want lower latency.
- `drift connect` is the only exception — it does `mosh <circuit>` (or `ssh -t`) + `devpod ssh <kart>` for the interactive terminal, outside the RPC path.

---

## Implementation stack

Locked-in picks for the Go implementation. Rationale is in commit history / research notes; this section is the contract that future work builds on. Target **Go 1.25** (with `toolchain` directive pinning a concrete patch version). All recommendations are current as of 2026.

### Repo layout

Single Go module at repo root (`github.com/kurisu-agent/drift`). Both binaries, one `go.mod`, same release cadence, same semver. No `go.work` (workspaces are for multi-module setups; we don't have one). No `pkg/` directory — the `golang-standards/project-layout` convention is not followed in 2026; the Go team's own [*Organizing a Go module*](https://go.dev/doc/modules/layout) guide is the reference.

```
drift/
  go.mod                     # module github.com/kurisu-agent/drift
  cmd/
    drift/main.go            # <30 lines; builds root ctx, parses flags, dispatches
    lakitu/main.go           # same pattern
  internal/
    wire/                    # JSON-RPC 2.0 request/response/error types
    rpc/                     # dispatcher + method registry (shared)
    rpcerr/                  # typed error + Go-error → JSON-RPC-error mapping
    cli/
      drift/                 # Kong struct for drift subcommands
      lakitu/                # Kong struct for lakitu subcommands
    sshconf/                 # ~/.config/drift/ssh_config management
    exec/                    # thin wrapper around os/exec for ssh/mosh/docker/devpod
    config/                  # YAML config loader
    version/                 # ldflag receivers (Version, Commit, Date)
  flake.nix                  # NixOS module + devShell
  .goreleaser.yaml
  .golangci.yml
  .github/workflows/ci.yml
```

### Library picks

| concern        | pick                                      | notes                                                                                             |
|----------------|-------------------------------------------|---------------------------------------------------------------------------------------------------|
| CLI framework  | `alecthomas/kong`                         | struct-tag driven, no globals, good help/completion. Cobra rejected (globals, codegen overhead).  |
| JSON-RPC 2.0   | **hand-rolled** in `internal/wire/`       | one-shot stdio doesn't need a connection-oriented library. Reference `creachadair/jrpc2` for dispatcher ergonomics. `net/rpc/jsonrpc` is frozen/non-compliant — do not use. |
| Logging        | stdlib `log/slog`                         | TextHandler to **stderr** by default; JSON via `--log-format=json`. See stdout invariant below.    |
| Errors         | stdlib `errors` + typed `rpcerr.Error`    | `errors.Is/As/Join`, wrap with `%w` internally, convert to JSON-RPC error exactly once at the dispatch boundary. No third-party error library. |
| Config         | `gopkg.in/yaml.v3` + struct tags + `Validate()` | viper explicitly rejected (bloat, key-lowercasing, magic). Koanf v2 if we outgrow stdlib.     |
| Testing        | stdlib `testing` + `rogpeppe/go-internal/testscript` + `google/go-cmp` | testscript for CLI golden tests; `testing/synctest` (stable in 1.25) for concurrency; fuzz the wire decoder. No testify. |
| Lint           | `golangci-lint` v2                        | Start from `default: standard`; add `errorlint`, `staticcheck`, `gosec`, `revive`, `errcheck`, `govet`, `ineffassign`, `unused`, `misspell`, `bodyclose`, `nolintlint`, `copyloopvar`. |
| Vuln           | `govulncheck`                             | PR blocker + weekly cron on `main`.                                                               |
| Release        | GoReleaser v2 + separate Nix `buildGoModule` | parallel tracks, never try to unify. `-trimpath`, `mod_timestamp: {{.CommitTimestamp}}`, `-X internal/version.*` ldflags for reproducible builds. |
| Static binary  | `CGO_ENABLED=0`                           | sufficient on its own in 2026 — no `netgo`/`osusergo` tags needed. Lakitu is Linux-only; drift builds for macOS + Linux, amd64 + arm64. |

### Critical invariants (mechanically tested)

- **lakitu never writes non-JSON-RPC to stdout.** stdout is the RPC response channel; logs go to stderr. A testscript assertion guards this from day one — violating it corrupts every client call.
- **Every I/O-touching function takes `ctx context.Context` as its first parameter.** Root context built in `main` via `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)` so Ctrl-C cascades through `exec.CommandContext` children.
- **`exec.CommandContext` + `cmd.Cancel` + `cmd.WaitDelay`** for every external process (ssh, mosh, docker, devpod). SIGTERM first, SIGKILL after 5s. Never invoke a shell — build argv directly.
- **Panics only on programmer error.** lakitu's `main` wraps a top-level `recover()` that formats a `-32603 internal error` response and exits non-zero; a mid-handler panic must not leave stdout half-written.

### 2026 Go idioms to lean on

`cmp.Or`, `errors.Join`, `sync.OnceValue(s)`, `slices`/`maps` packages, `min`/`max` built-ins, range-over-func iterators where they fit naturally, `t.Context()` / `t.Chdir()` in tests, `debug.ReadBuildInfo()` as the `--version` fallback when ldflags aren't injected.

### Explicitly rejected

- **viper** — dep bloat, silently lowercases keys, magic precedence rules.
- **cobra** — globals, generator scaffolding, surface area we don't need.
- **testify** — `cmp.Diff` produces better failure messages; stdlib assertions are enough.
- **`net/rpc`/`net/rpc/jsonrpc`** — frozen, not JSON-RPC 2.0 compliant.
- **`pkg/` directory** — nothing is exported to external consumers; `internal/` is the right home.
- **`go.work`** — one module, no need.
- **GOEXPERIMENT=jsonv2** — revisit when it lands without the experiment flag; our RPC volume doesn't need the perf.

---

## Transport and authentication

All client ↔ server communication runs over **plain SSH**, carrying **JSON-RPC 2.0** as the application protocol. There is no custom wire format, no drift daemon listening on a TCP port, and no bespoke auth layer:

- Every non-local `drift` subcommand resolves to `ssh drift.<circuit> lakitu rpc`, with a JSON-RPC request piped to stdin and a JSON-RPC response read from stdout. See [RPC protocol](#rpc-protocol). The `drift.<circuit>` alias is drift-managed — see [SSH config management](#ssh-config-management).
- `drift connect` adds a second leg: `mosh drift.<circuit>` (or `ssh -t`) to land on the circuit, then `devpod ssh <kart>` to enter the container. The mosh/ssh leg is the only non-RPC SSH usage.

**Authentication and authorization are out of scope for drift.** Whatever makes `ssh drift.<circuit>` succeed on the user's workstation — OpenSSH keys in `~/.ssh/`, an SSH agent, a YubiKey, certificates, an SSH CA, a jumphost, Tailscale SSH, a corporate bastion, `Match` rules in `sshd_config`, etc. — is what drift uses. drift never asks for a password and never manages keys. Its only modification to the user's SSH setup is a single managed `Include` line (detailed below), so per-circuit Host aliases can carry ControlMaster for fast subsequent calls.

The circuit's existing Unix user/group permissions on `~/.drift/garage/` are the only authorization model — if SSH lets you in as user `X`, you have full access to X's karts, characters, and chest entries.

---

## SSH config management

drift manages a **small, well-bounded slice** of the user's SSH setup — enough to give every circuit a named host alias with ControlMaster enabled by default, so RPC calls and `drift connect` alike are fast on second and subsequent invocations. drift does NOT manage keys, identities, or auth — that stays pure OpenSSH user territory.

### Files drift owns

```
~/.config/drift/
  ssh_config           # one Host drift.<circuit> block per circuit; fully drift-managed
  sockets/             # ControlMaster Unix sockets (dir mode 0700)
```

### The only line drift adds to ~/.ssh/config

On the first `drift circuit add`, drift ensures this line exists **at the top** of `~/.ssh/config` (creates the file with mode 0600 if absent). It never edits any other line in that file.

```
Include ~/.config/drift/ssh_config
```

Placing the include at the top means drift's `Host drift.<name>` blocks match before any user `Host *` globals, so ControlMaster settings stick even if the user has `ControlMaster no` as a global default.

### Generated Host blocks

Two kinds of blocks go into `~/.config/drift/ssh_config`: one literal block per circuit, plus a single wildcard block for per-kart IDE routing.

**Per-circuit block** — circuit `my-server` with `host: dev@my-server.example.com` becomes:

```sshconfig
Host drift.my-server
  HostName my-server.example.com
  User dev
  ControlMaster auto
  ControlPath ~/.config/drift/sockets/cm-%r@%h:%p
  ControlPersist 10m
  ServerAliveInterval 30
  ServerAliveCountMax 3
```

- **`ControlMaster auto` + `ControlPersist 10m`** — first drift call opens a persistent TCP/SSH master, subsequent calls (RPC and `drift connect`) reuse it. Typical warm-call latency: ~10–30 ms vs ~200–400 ms cold.
- **`ServerAlive*`** — keeps the master healthy across flaky networks and NAT idle timeouts. mosh itself is UDP so this doesn't affect it, but the cold ssh fallback and the port-forwarding leg benefit.

**Per-kart wildcard block** — written once, at the *end* of `~/.config/drift/ssh_config` so it matches only when no literal per-circuit block hits:

```sshconfig
Host drift.*.*
  ProxyCommand drift ssh-proxy %h %p
  ControlMaster auto
  ControlPath ~/.config/drift/sockets/cm-%r@%h:%p
  ControlPersist 10m
```

OpenSSH is first-match, so literal `Host drift.my-server` blocks win for bare circuit aliases. The wildcard only matches when there are at least two dots after `drift.`, i.e. `drift.<circuit>.<kart>`.

### Per-kart SSH aliases (`drift.<circuit>.<kart>`)

`ssh drift.my-server.kurisu-api` behaves as a plain SSH connection directly into the container — the alias is a first-class SSH target for anything that reads `~/.ssh/config`:

- **IDEs:** VS Code Remote-SSH, JetBrains Gateway, Cursor, Windsurf — pick `drift.<circuit>.<kart>` from the host list, connect, the IDE installs its remote agent into the container, done. No drift-specific IDE plugin required.
- **File-transfer tooling:** `scp`, `rsync`, `sshfs`, `sftp` — `scp ./file drift.my-server.kurisu-api:/workspace/` just works.
- **Anything else that speaks SSH** — including CI agents, remote debuggers, language server remote modes.

This is the same pattern [Coder](https://github.com/coder/coder) uses for their workspaces, and the reason it's in MVP rather than Future: without it, IDE integration forces an IDE-specific plugin per IDE.

`drift connect` (mosh-based) remains the preferred way to open an interactive terminal session — faster (UDP), resilient to network changes. The wildcard alias is primarily for tooling that only speaks SSH.

#### How `drift ssh-proxy` works

`drift ssh-proxy <alias> <port>` is invoked by OpenSSH as the ProxyCommand. It:

1. Parses `<alias>` → circuit name + kart name.
2. Looks up `circuits.<circuit>` in drift's config for the underlying SSH target.
3. Opens an SSH session to the circuit via the managed `drift.<circuit>` alias (inheriting ControlMaster multiplexing), running `devpod ssh <kart> --stdio` on the far side.
4. Pipes its own stdin/stdout to that remote process — effectively tunneling the outer SSH handshake through to the container's devpod-injected SSH server.

The outer `ssh drift.<circuit>.<kart>` process speaks SSH with the container's SSH server end-to-end; drift ssh-proxy is just a transparent stdio pipe. Auth at the container layer is handled by devpod's injected SSH server (a generated per-workspace keypair); auth at the circuit layer is the user's own SSH credentials.

### Usage

All drift internals use the appropriate alias:
- **RPC:** `ssh drift.<circuit> lakitu rpc`
- **Connect (interactive terminal):** `mosh drift.<circuit> -- devpod ssh <kart>` (fallback: `ssh -t drift.<circuit> ...`)
- **Per-kart SSH (IDEs, scp, rsync):** `ssh drift.<circuit>.<kart>` — handled by `drift ssh-proxy` under the hood

Users get all three for free once a circuit is added.

### Lifecycle

| event                                            | `~/.ssh/config`                  | `~/.config/drift/ssh_config`                          |
|--------------------------------------------------|----------------------------------|-------------------------------------------------------|
| `drift circuit add my-server --host dev@...`     | ensure Include line at top       | append `Host drift.my-server` block                   |
| `drift circuit add my-server ...` (re-run)       | ensure Include line at top       | replace the existing block in place (idempotent)      |
| `drift circuit rm my-server`                     | untouched                        | remove `Host drift.my-server` block                   |

### Opt-out / override

- `manage_ssh_config: false` in `~/.config/drift/config.yaml` disables all ssh_config writes. drift then passes `user@host` directly to `ssh`/`mosh`, losing ControlMaster speedup.
- Users who want to customize the block can add overrides to `~/.ssh/config` **before** the drift `Include` line (e.g. their own `Host drift.* IdentityFile ~/.ssh/id_work`). OpenSSH is first-match, so earlier user entries win.

---

## RPC protocol

drift↔lakitu communication uses **[JSON-RPC 2.0](https://www.jsonrpc.org/specification)** as the wire protocol, carried over a plain SSH channel. drift invokes lakitu once per operation through a single RPC entry point:

```
ssh <circuit> lakitu rpc
```

drift writes a JSON-RPC request to stdin; `lakitu rpc` reads it, dispatches to a method handler, writes a JSON-RPC response to stdout, and exits. SSH carries transport and auth; JSON-RPC carries the semantic request/response. No custom framing, no binary protocol, no daemon.

### Request

```json
{
  "jsonrpc": "2.0",
  "method": "kart.new",
  "params": {
    "name": "myproject",
    "clone": "git@github.com:user/repo.git",
    "tune": "node",
    "character": "kurisu",
    "autostart": true
  },
  "id": 1
}
```

- All methods use **named parameters** (`params` is an object, never an array).
- `id` is always set — drift does not send notifications.

### Response (success)

```json
{
  "jsonrpc": "2.0",
  "result": { "...method-specific payload..." },
  "id": 1
}
```

### Response (error)

```json
{
  "jsonrpc": "2.0",
  "error": {
    "code": 3,
    "message": "kart 'myproject' not found",
    "data": { "type": "kart_not_found", "kart": "myproject" }
  },
  "id": 1
}
```

The `error` object shape (`code` / `message` / `data`) is defined in [Error handling](#error-handling).

### Process lifecycle

- **MVP: one request per SSH invocation.** `lakitu rpc` reads exactly one request, writes one response, exits. Simple and stateless; OpenSSH's `ControlMaster` provides TCP connection reuse when desired.
- **Future: `lakitu serve`** — long-lived stdin/stdout session for pipelining, server-initiated notifications (streaming logs, progress), and JSON-RPC batching. Not in MVP.

### Method catalog

All methods are namespaced by resource. Each method has a direct human-CLI counterpart on lakitu (same handler, different I/O wrapper).

| method             | human CLI                 | notes                                            |
|--------------------|---------------------------|--------------------------------------------------|
| `server.version`   | `lakitu version`          | returns `{"version": "<semver>", "api": <int>}`; used for compat check |
| `server.init`      | `lakitu init`             | bootstrap garage; idempotent                     |
| `kart.new`         | `lakitu new`              | returns kart info on success                     |
| `kart.start`       | `lakitu start`            | idempotent                                       |
| `kart.stop`        | `lakitu stop`             | idempotent                                       |
| `kart.restart`     | `lakitu restart`          |                                                  |
| `kart.delete`      | `lakitu delete`           | errors on missing                                |
| `kart.list`        | `lakitu list`             | returns array of kart info                       |
| `kart.info`        | `lakitu info`             | returns single kart info ([schema](#lakitu-info-kart--json-schema)) |
| `kart.enable`      | `lakitu enable`           | idempotent                                       |
| `kart.disable`     | `lakitu disable`          | idempotent                                       |
| `kart.logs`        | `lakitu logs`             | returns a chunk; streaming is future             |
| `character.add`    | `lakitu character add`    |                                                  |
| `character.list`   | `lakitu character list`   |                                                  |
| `character.show`   | `lakitu character show`   |                                                  |
| `character.remove` | `lakitu character rm`     | errors if any kart references it                 |
| `chest.set`        | `lakitu chest set`        | value in `params.value`                          |
| `chest.get`        | `lakitu chest get`        |                                                  |
| `chest.list`       | `lakitu chest list`       | never returns values                             |
| `chest.remove`     | `lakitu chest rm`         |                                                  |
| `tune.list`        | `lakitu tune list`        |                                                  |
| `tune.show`        | `lakitu tune show`        |                                                  |
| `tune.set`         | `lakitu tune set`         | creates or updates                               |
| `tune.remove`      | `lakitu tune rm`          | errors if any kart references it                 |
| `config.show`      | `lakitu config show`      | returns server-level config                      |
| `config.set`       | `lakitu config set`       |                                                  |

### Transport semantics

- SSH process exit `0`: RPC response delivered (success **or** error). drift reads the response and branches on `result` vs `error`.
- SSH process exit `!= 0` (typically `255`): **transport failure** (unreachable host, auth denied, lakitu crashed before writing a response). drift passes OpenSSH's stderr through verbatim and exits non-zero without fabricating a JSON-RPC envelope.
- stdout carries **exactly one JSON object** per `lakitu rpc` invocation (the JSON-RPC response), newline-terminated.
- stderr may carry devpod's own output for debugging; drift logs it under `--debug` but does not parse it.

### Human-facing CLI vs RPC

`drift` always uses the RPC path. Humans running `lakitu` directly on a circuit keep the named subcommands (`lakitu new myproject`, `lakitu list`, etc.) — each parses argv, builds the equivalent RPC request, dispatches to the same handler as `lakitu rpc`, and formats the result for the terminal. Errors on the human path follow the [stderr format](#stderr-format) in Error handling.

Shared Go code (`drift/internal/wire/`) defines the method names, parameter structs, and result structs used by both paths.

---

## Naming

### Binaries

| binary      | role                                    |
|-------------|-----------------------------------------|
| **`drift`** | client CLI, runs on workstations        |
| **`lakitu`**| server daemon, runs on each circuit     |

### Concepts

| term           | meaning                                              |
|----------------|------------------------------------------------------|
| **kart**       | a devpod workspace / dev container                   |
| **circuit**    | a remote dev server                                  |
| **character**  | a git/GitHub identity profile                        |
| **tune**       | named preset of default flags for kart creation      |
| **starter**    | a template git repo (history stripped on clone)      |
| **garage**     | server-side state dir (`~/.drift/garage/`)           |
| **chest**      | pluggable secret store on the circuit                |

### Commands

| command              | meaning                                            |
|----------------------|----------------------------------------------------|
| **`drift warmup`**   | interactive first-time setup (circuits + characters) |
| **`drift new`**      | create a new kart (starter or clone)               |
| **`drift connect`**  | connect to a kart (auto-starts if stopped)         |
| **`drift start`**    | start a stopped kart                               |
| **`drift stop`**     | stop a running kart                                |
| **`drift restart`**  | stop then start a kart                             |
| **`drift delete`**   | remove a kart entirely                             |
| **`drift list`**     | list all karts and status                          |
| **`drift enable`**   | auto-start kart on server reboot                   |
| **`drift disable`**  | disable auto-start                                 |
| **`drift circuit`**  | manage remote servers                              |
| **`drift character`**| manage git/GitHub identity profiles                |
| **`drift chest`**    | manage secrets on the circuit                      |
| **`drift ssh-proxy`**| internal ProxyCommand helper for `drift.<circuit>.<kart>` aliases |

---

## CLI design

### `drift` (client)

All commands except `circuit` (client-local config) and `connect` (mosh/ssh terminal) dispatch as a JSON-RPC call to `lakitu rpc` over SSH — see [RPC protocol](#rpc-protocol) for the method catalog.

```
drift warmup                    — interactive first-time setup (circuits + characters)
drift new     <name>  [flags]   — create a new kart (from starter or existing repo)
drift connect <kart>  [flags]   — connect (mosh preferred, ssh fallback); auto-starts if stopped
drift start   <kart>            — start a stopped kart
drift stop    <kart>            — stop a running kart
drift restart <kart>            — stop then start
drift delete  <kart>            — remove kart
drift list                      — list karts and status
drift enable  <kart>            — auto-start on server reboot
drift disable <kart>            — disable auto-start
drift circuit   [list|add|rm]       — manage circuits (client-side config + ~/.ssh/config)
drift character [list|add|show|rm]  — manage identity profiles (server-side)
drift chest     [set|get|list|rm]   — manage secrets (server-side)
```

> **Deferred to a later phase:** ports management (`drift ports` — view/add/remove declared ports, standalone forwarding, conflict detection, per-workstation remap persistence). MVP relies on devpod's own `-L` forwarding invoked ad-hoc if needed, but drift exposes no port-management UX. See [Future](#future).

Global flags (all commands):
```
--circuit, -c <name>    target circuit (overrides default)
--debug                 verbose output
--skip-version-check    bypass drift↔lakitu semver check (see Version compatibility)
```

#### `drift warmup`

Interactive setup wizard for first-time drift users. Re-runnable: each invocation can add more circuits or characters without re-doing earlier steps. Walks through:

1. **Circuits**
   - Prompt for circuit name (validates against kart-name regex) and SSH target (`user@host[:port]`).
   - Offer to set as default circuit.
   - Write the managed SSH config entries (see [SSH config management](#ssh-config-management)).
   - Probe: call `server.version` RPC, print round-trip latency, confirm lakitu is installed and compatible.
   - On probe failure: print install hints (Nix module + manual tarball), offer retry / skip / "edit and continue".
2. **Characters** (only offered once at least one circuit probes OK)
   - Ask which circuit to attach the character to (default preselected when only one).
   - Prompt for character name, git name, git email, GitHub username (optional), SSH key path (optional).
   - Offer to stage a PAT: walks the user through `chest.set`, then stores `chest:<name>` as the character's `pat` field.
   - Offer to set as the circuit's `default_character`.
3. **Summary**
   - Lists configured circuits with last-probe latency and lakitu version.
   - Lists characters created.
   - Prints a suggested next command: `drift new my-first-kart`.

Flags:
```
--skip-circuits     skip the circuit phase (assume already configured)
--skip-characters   skip the character phase
--no-probe          skip the server.version probe (offline setup)
```

Interactive only — returns a `user_error` (exit code 2) if stdin isn't a TTY. Scripted equivalents are `drift circuit add` + `drift character add` + `drift chest set`.

#### `drift new` flags

```
--clone   <git-url>        clone an existing repo (mutually exclusive with --starter)
--starter <git-url>        template repo; history is discarded after clone
--tune    <name>           named preset (provides defaults for other flags)
--features <json>          devcontainer features JSON, injected via devpod
                           --additional-features (additive, merged last)
--devcontainer <src>       override devcontainer: JSON string, file path, or URL
                           passed as devpod --extra-devcontainer-path
--dotfiles <git-url>       layer-2 dotfiles repo (overrides tune's dotfiles_repo)
--character <name>         git/github identity to inject
--autostart                enable auto-start on server reboot
```

`--clone` and `--starter` are mutually exclusive. With neither, defaults from the active tune apply.
Kart name is always the positional `<name>` argument.

**Name collision:** `drift new <name>` **fails** if a kart with that name already exists on the circuit. No overwrite, no confirmation prompt — user must `drift delete <name>` first, or use `drift start <name>` / `drift connect <name>` if they want to resume the existing one.

**Kart name validation:** lowercase alphanumeric + hyphen, 1–63 chars, must start with a letter. Regex: `^[a-z][a-z0-9-]{0,62}$`. Reserved names: `default`, `none` (collide with reserved tune values — see [Flag composition](#flag-composition-and-resolution)).

#### `drift connect` flags

```
--ssh                     force plain SSH (skip mosh)
--forward-agent           enable SSH agent forwarding (-A) into the circuit
```

Port-forwarding flags are deferred to the ports phase.

#### `drift circuit add` flags

```
--host <user@host>    SSH destination
--default             set as default circuit
--no-ssh-config       skip writing to ~/.ssh/config and ~/.config/drift/ssh_config for this circuit
```

Adding a circuit also writes a `Host drift.<name>` block and ensures the `Include` line in `~/.ssh/config` — see [SSH config management](#ssh-config-management).

#### `drift circuit rm <name>`

Removes the circuit from `~/.config/drift/config.yaml` and deletes its `Host drift.<name>` block from `~/.config/drift/ssh_config`. Leaves `~/.ssh/config` untouched (the `Include` line stays, since other circuits may still need it).

#### `drift chest`

Pluggable secret store on the circuit. **MVP backend is a plain YAML file** (no encryption) — the goal is to ship the interface and iterate on backends (age, 1Password, Vault, etc.) later without breaking the CLI surface. YAML was chosen over `.env` because secrets frequently span multiple lines (SSH keys, PEM-encoded PATs piped through helpers) and `.env` quoting rules are an ongoing footgun.

```
drift chest set  <name>         — prompt for value (never passed as a flag)
drift chest list                — list secret names (values never shown)
drift chest rm   <name>         — remove a secret
drift chest get  <name>         — print value (scripting; opt-in per-call)
```

Values are sent via stdin over the SSH pipe from `drift chest set` to `lakitu chest set`; they are never passed as a flag argument or positional.

Secrets are referenced elsewhere as `chest:<name>`:

```sh
drift character add kurisu --pat chest:github-pat
```

**Backend interface (Go):**

```go
type ChestBackend interface {
    Set(name string, value []byte) error
    Get(name string) ([]byte, error)
    List() ([]string, error)
    Remove(name string) error
}
```

The active backend is selected in the server's `config.yaml` under the `chest:` key. MVP implements `yamlfile` (reads/writes `~/.drift/garage/chest/secrets.yaml`, mode 0600; top-level map of `name: value`, multi-line values via block scalars).

#### `drift character add` flags

```
--name    <str>           git author name
--email   <str>           git author email
--github  <str>           github username
--pat     chest:<name>    reference to a PAT stored in the chest
--ssh-key <path>          path to SSH private key file on the server
```

---

### `lakitu` (server)

Runs locally on the circuit. Two invocation modes, same handlers:
- **Human CLI:** named subcommands below — `lakitu new myproject`, `lakitu list`, etc. For direct circuit administration.
- **RPC:** `lakitu rpc` reads one JSON-RPC 2.0 request from stdin, writes one response to stdout, exits. This is the path `drift` uses over SSH.

```
lakitu new     <name>  [flags]  — create kart (same flags as drift new)
lakitu start   <kart>           — start a stopped kart (devpod up <kart>, idempotent)
lakitu stop    <kart>           — stop
lakitu restart <kart>           — stop then start
lakitu delete  <kart>           — remove
lakitu list                     — list
lakitu info   <kart>            — JSON kart info (used by drift connect)
lakitu enable  <kart>           — autostart on
lakitu disable <kart>           — autostart off
lakitu logs   <kart>            — systemd journal for kart service
lakitu version                  — print semver version string (for compat check)
lakitu init                     — idempotent first-run setup of ~/.drift/garage/
lakitu rpc                      — read one JSON-RPC request on stdin, write response on stdout (see RPC protocol)
lakitu character [list|add|show|rm]
lakitu tune    [list|show|set|rm]
lakitu chest   [set|get|list|rm]
lakitu config  [show|set]              — server-level config
```

#### `lakitu info <kart>` — JSON schema

Called by `drift connect` (and any other client flow that needs to materialize kart state). Stable contract — additive changes only.

```json
{
  "name": "myproject",
  "status": "running",
  "created_at": "2026-04-17T12:34:56Z",
  "source": {
    "mode": "clone",
    "url": "https://github.com/user/myproject"
  },
  "tune": "node",
  "character": "kurisu",
  "autostart": true,
  "container": {
    "user": "vscode",
    "shell": "/bin/zsh",
    "workdir": "/workspaces/myproject",
    "image": "mcr.microsoft.com/devcontainers/base:ubuntu"
  },
  "devpod": {
    "workspace_id": "myproject",
    "provider": "docker"
  }
}
```

Field semantics:
- `status` — one of `running`, `stopped`, `busy`, `error`, `not_found`. `busy` covers transitional states (starting, stopping).
- `source.mode` — `clone` | `starter` | `none` (`none` = scratch scaffold).
- `source.url` — absent when `source.mode == "none"`.
- `character` — empty string when no character is attached.
- `container.*` — reflects the running container; may be absent when `status != "running"`.
- Consumers **must** tolerate unknown top-level or nested fields (forward compat).

Error shape (non-zero exit from `lakitu info`):
```json
{ "error": "kart not found", "kart": "myproject" }
```

---

## Version compatibility

drift and lakitu are released together with a shared **semver** version.

On each `drift` invocation that contacts a circuit, drift issues a `server.version` RPC (cached per-session — see open question below) and compares to its own version. The response shape:

```json
{ "version": "1.4.2", "api": 1 }
```

- `version` — lakitu's semver string. Drives the major/minor/patch comparison below.
- `api` — integer schema version for the RPC surface itself. Bumped only on breaking wire changes (removed methods, changed required params). Lets drift refuse a lakitu that's still semver-compatible on paper but speaks an older JSON-RPC surface. MVP ships `api: 1`.

Comparison rules:

| comparison            | behavior                   |
|-----------------------|----------------------------|
| **major** mismatch    | error, abort               |
| **minor** mismatch    | warning to stderr, continue |
| **patch** mismatch    | silent, continue           |

`--skip-version-check` bypasses the check entirely (needed during upgrades and local testing).

The `lakitu info` JSON schema is versioned by the lakitu semver — additive-only changes within a major.

---

## Error handling

Errors surface in two places depending on who's calling lakitu:

1. **drift (RPC path):** errors appear as the `error` field of a JSON-RPC 2.0 response on stdout (see [RPC protocol](#rpc-protocol)). SSH exit code is still `0` because the response was delivered — drift branches on the response shape.
2. **Humans (direct CLI):** `lakitu <subcommand>` emits a one-line human summary to stderr followed by the same error object as JSON on stderr, and exits with a non-zero code.

Both paths use the same `error` object — defined here — serialized from the same Go type.

### Error object

```json
{
  "code": 3,
  "message": "kart 'myproject' not found",
  "data": {
    "type": "kart_not_found",
    "kart": "myproject",
    "circuit": "my-server"
  }
}
```

- `code` (int) — matches the process exit code (see below). One number, both on the wire and on the exit status.
- `message` (string) — stable human summary; echoed as the first stderr line.
- `data.type` (string) — stable snake_case identifier for programmatic branching (e.g. `kart_not_found`, `name_collision`, `devpod_up_failed`). Preferred over integer codes in client code paths.
- `data.*` — arbitrary extension fields carrying context (kart name, tune name, underlying devpod exit code, `suggestion` strings, etc.).

### `code` values (and exit codes on the human path)

Small, stable set. On the human CLI path, `code` doubles as the process exit code. On the RPC path, the SSH process still exits `0` — `code` lives only in the response.

| code | category       | typical `data.type` values                                           |
|------|----------------|----------------------------------------------------------------------|
| 0    | success        | — (not an error; `result` branch)                                    |
| 1    | unspecified    | `internal_error`                                                     |
| 2    | user error     | `invalid_name`, `invalid_flag`, `mutually_exclusive_flags`           |
| 3    | not found      | `kart_not_found`, `character_not_found`, `chest_entry_not_found`     |
| 4    | conflict       | `name_collision`, `stale_kart`, `already_enabled`                    |
| 5    | devpod error   | `devpod_up_failed`, `devpod_ssh_failed`, `devpod_unreachable`        |
| 6    | auth/perms     | `chest_backend_denied`, `garage_write_denied`, `systemd_denied`      |

SSH's own exit **255** is never used by drift or lakitu. When drift observes 255 from the `ssh` process, it treats the failure as a *transport* error and passes OpenSSH's stderr through verbatim (`ssh: Could not resolve hostname ...`) — no RPC response, no fabricated envelope.

### stderr format (human CLI path)

```
error: kart 'myproject' not found
  type: kart_not_found
  kart: myproject
```

Line 1: `error: ` + `message`. Following lines: `  <key>: <value>`, starting with `type` (the stable snake_case identifier scripts grep on) and then each entry in `data`, sorted by key for deterministic output. **stdout stays reserved** for structured command output (table renderings of `lakitu list`, JSON for `--output json`, etc.) and never carries error payloads. Exit code mirrors `code`.

The RPC path uses the same error object but wraps it in a JSON-RPC response envelope on stdout instead — see [RPC protocol](#rpc-protocol).

### Idempotency

Lifecycle verbs are idempotent — retries are safe, scripts don't have to branch on current state:

- `drift stop <running>` → 0. `drift stop <stopped>` → 0.
- `drift start <stopped>` → 0. `drift start <running>` → 0.
- `drift restart` → 0 regardless of starting state.
- `drift enable` when already enabled → 0. `drift disable` when already disabled → 0.
- `drift delete <missing>` → 3 (`kart_not_found`). Delete is the one verb that errors on missing, since silently succeeding would hide typos.

### Stale karts

If `lakitu` finds `garage/karts/<name>/` but `devpod list` doesn't know the workspace (crash mid-`drift new`, manual `devpod delete`), it emits:

```json
{
  "code": 4,
  "message": "kart 'myproject' is stale (garage state without devpod workspace)",
  "data": {
    "type": "stale_kart",
    "kart": "myproject",
    "suggestion": "drift delete myproject to clean up, then drift new myproject"
  }
}
```

### Interrupts

Client Ctrl-C closes the SSH channel; sshd sends SIGHUP to lakitu. lakitu's signal handler:

1. Cancels the in-flight devpod subprocess (SIGTERM, then SIGKILL after a short grace).
2. Removes kart-scoped tmpdirs (starter clones, layer-1 dotfiles scratch).
3. If the interrupted command was `lakitu new` and the kart dir was already written, writes a `status: error` marker so the next `drift new <same-name>` returns `stale_kart` (exit 4) rather than silently colliding on a corpse.

---

## Flag composition and resolution

When creating a kart, flags are resolved in this order (later wins):

```
1. server defaults (lakitu config)
   default_tune, default_character, nix_cache_url

2. tune preset (if --tune specified or default_tune set)
   provides: starter, features, devcontainer, dotfiles_repo

3. explicit flags (--starter, --features, --devcontainer, --character)
   always override tune values

4. --features is always ADDITIVE — merged on top of whatever devcontainer
   specifies, never replaces. Maps directly to devpod --additional-features.

5. --devcontainer fully overrides any devcontainer.json in the repo.
   Accepts: file path → passed as-is to devpod --extra-devcontainer-path
            JSON string → written to temp file, then passed
            URL → downloaded to temp file, then passed
```

**Reserved tune values (cannot be used as user-created tune names):**
- `--tune none` — disables all tune defaults (no starter, no features, no dotfiles).
- `--tune default` — resolves to the tune literally named `default`. Implicit when `--tune` is omitted.

---

## Kart creation modes

```
drift new myproject                            → starter from default tune (or empty scaffold)
drift new myproject --starter <url>            → scaffold from git url, strip history
drift new myproject --clone <url>              → clone existing repo
drift new myproject --clone <url> --tune node  → clone + apply tune
```

Under the hood both call `devpod up --provider docker --ide none` on the server with the resolved flags.

### Starter history strip

When `--starter <url>` is used, lakitu:

1. `git clone <url> <tmpdir>`
2. `rm -rf <tmpdir>/.git`
3. `cd <tmpdir> && git init && git add . && git commit -m "Initial commit from starter <url>"`
4. Pass `<tmpdir>` as the positional source to `devpod up`.

The initial commit author is set from the active character (falls back to `drift <noreply@drift.local>` when no character is configured). Submodules are **not** preserved — `.gitmodules` references survive as text, but `.git/modules` is gone after the strip.

---

## Dotfiles injection

drift applies dotfiles in **two layers**, both run during `devpod up`. Layer 2 runs after layer 1, so user files override drift's defaults.

### Layer 1 — drift-controlled (character layer)

Generated by lakitu at kart-create time from the attached character. Sets up identity and drift-internal baseline:

- `~/.gitconfig` — `user.name`, `user.email`, `github.user`
- `~/.config/gh/hosts.yml` — when a PAT is attached via chest
- git credential helper — injects the PAT for HTTPS clones/pushes
- `~/.ssh/id_<alg>` + `~/.ssh/config` entry — when character has `ssh_key_path`
- Any shell hooks drift needs (PATH for lakitu-installed tooling, etc.)

Implementation: lakitu writes the script + files to a kart-scoped scratch dir, then invokes `devpod agent workspace install-dotfiles --dotfiles file://<path>` (exact mechanism TBD — may be direct `devpod ssh --command` post-`up` if install-dotfiles proves awkward for local paths).

### Layer 2 — user overrides

The user's dotfiles repo from the active tune (`dotfiles_repo`) or `--dotfiles <url>` on `drift new`. Passed to devpod as `--dotfiles <url>`. Runs after layer 1, so user's `~/.zshrc`, `~/.gitconfig.local`, etc. can override drift's defaults.

---

## Server state layout

```
~/.drift/garage/
  config.yaml                   server-level defaults
  tunes/
    default.yaml
    node.yaml
    python.yaml
  characters/
    <name>.yaml                 git_name, git_email, github_user, ssh_key_path, pat_secret
  chest/
    secrets.yaml                MVP backend — top-level name:value map, mode 0600
  karts/
    <name>/
      config.yaml               repo, tune, character, source_mode, user, shell
      autostart                 presence = auto-start enabled
```

### `config.yaml` fields

```yaml
default_tune: default
default_character: ""
nix_cache_url: ""

chest:
  backend: yamlfile     # yamlfile (MVP) | age | onepassword | vault (future)
  # backend-specific config lives under this key
```

Settable via `lakitu config set` (and the `config.set` RPC): `default_tune`, `default_character`, `nix_cache_url`, `chest.backend`. Backend-specific subkeys under `chest.*` are set the same way (e.g. `lakitu config set chest.yamlfile.path ...` for the MVP backend). Unknown keys are rejected with `code: 2` (`invalid_flag`).

### Character file (`characters/<name>.yaml`)

```yaml
git_name: Kurisu Makise
git_email: kurisu@example.com
github_user: kurisu              # optional
ssh_key_path: ~/.ssh/id_ed25519  # optional; path on the circuit
pat_secret: chest:github-pat     # optional; chest reference, never a literal token
```

Only `git_name` and `git_email` are required. `pat_secret` always takes the `chest:<name>` form — literal tokens are rejected at `character add` time.

### Tune profile fields (all optional)

```yaml
starter: https://github.com/org/starter-repo
devcontainer: https://example.com/devcontainer.json
dotfiles_repo: https://github.com/org/dotfiles
# features stays as JSON — passed directly to devpod --additional-features
features: '{"ghcr.io/devcontainers/features/node:1": {"version": "lts"}}'
```

---

## devpod integration

devpod is invoked exclusively by `lakitu` on the circuit. The `drift` client has no dependency on devpod and does not need it installed.

### Commands used by lakitu

| operation           | devpod command                                                            |
|---------------------|---------------------------------------------------------------------------|
| create / start kart | `devpod up --provider docker --ide none [flags] <src>`                    |
| stop kart           | `devpod stop <name>`                                                      |
| delete kart         | `devpod delete --force <name>`                                            |
| kart status         | `devpod status <name> --output json`                                      |
| connect             | `devpod ssh <name> [--command cmd] [--user user]`                         |
| list workspaces     | `devpod list --output json`                                               |
| install dotfiles    | `devpod agent workspace install-dotfiles --dotfiles <url>`                |
| stream logs         | `devpod logs <name>`                                                      |

### Useful `devpod up` flags

```
--provider docker                       use local docker (circuit is host + controller)
--ide none                              no IDE backend — drift manages connections
--additional-features <json>            inject devcontainer features
--extra-devcontainer-path <file>        overlay devcontainer.json (docker provider only)
--dotfiles <url>                        dotfiles repo (layer 2)
--devcontainer-image <image>            override container image
--fallback-image <image>                image when no devcontainer config found
--git-clone-strategy <strategy>         git checkout strategy
--configure-ssh                         update ~/.ssh/config with workspace entry
```

### Useful `devpod ssh` flags

```
-L <local:host:remote>                  forward local port to container (deferred phase)
--command <cmd>                         run command instead of interactive shell
--user <user>                           container user
--workdir <path>                        working directory in container
--send-env <VAR>                        forward local env var into container
--set-env KEY=VALUE                     set env var in container
--ssh-keepalive-interval <duration>     keepalive frequency (default 55s)
```

### Flag mapping (drift → devpod)

| drift / tune field             | devpod flag                                                                  |
|--------------------------------|------------------------------------------------------------------------------|
| `--clone <url>`                | positional source arg to `devpod up`                                         |
| `--starter <url>`              | positional source arg to `devpod up` (post-history-strip tmpdir)             |
| `--features <json>`            | `--additional-features <json>`                                               |
| `--devcontainer <src>`         | `--extra-devcontainer-path <file>` — JSON string or URL resolved to temp file first |
| character identity             | layer-1 dotfiles (see [Dotfiles injection](#dotfiles-injection))             |
| tune `dotfiles_repo`           | `--dotfiles <url>` (layer 2)                                                 |
| character PAT (`chest:<name>`) | decrypted from chest at kart-create time; written into layer-1 git credential helper / `gh auth` |

### Provider

lakitu configures devpod to use the local `docker` provider — it manages containers directly on the circuit. The circuit is both the devpod controller and the container host.

### SSH server

devpod injects an SSH server into every container during `devpod up`. `drift connect` uses `devpod ssh <kart>` on the circuit to reach it — no `docker exec` required.

---

## Client config layout

```
~/.config/drift/
  config.yaml          user-edited circuit list + preferences
  ssh_config           drift-managed SSH Host blocks (see SSH config management)
  sockets/             ControlMaster Unix sockets (0700)
```

`config.yaml`:
```yaml
default_circuit: my-server
manage_ssh_config: true        # default; set false to skip ~/.ssh/config Include + per-circuit blocks
circuits:
  my-server:
    host: dev@my-server.example.com
  other:
    host: dev@other.example.com
```

---

## Connection flow (`drift connect`)

1. Client runs version check: `server.version` RPC (via `ssh drift.<circuit> lakitu rpc`) → compare to drift version.
2. Client fetches kart info: `kart.info` RPC → result ([schema](#lakitu-info-kart--json-schema)).

**mosh path (preferred):**

```
mosh drift.<circuit> -- devpod ssh <kart>
  (mosh lands on circuit via the drift-managed SSH alias, devpod ssh tunnels into container)
  (UDP session — interactive terminal, survives network changes)
```

**SSH fallback (no mosh on client):**

```
ssh -t drift.<circuit> "devpod ssh <kart>"
```

Agent forwarding (`-A`) is **off by default**. Enable with `--forward-agent` on `drift connect` as an explicit opt-in.

Port forwarding is deferred to the ports phase.

---

## Auto-start on reboot

Each enabled kart gets a systemd user service:

```
~/.config/systemd/user/lakitu-kart@.service   (template unit)
```

`lakitu enable <kart>` → `systemctl --user enable --now lakitu-kart@<kart>`

Server requires `loginctl enable-linger <user>` (set once via NixOS module or manual bootstrap).

Service runs: `lakitu start <kart>` — reads saved config from the garage and calls `devpod up <kart>`. Idempotent, safe to re-run.

---

## Bootstrap / install

### lakitu (on each circuit)

Two supported install paths:

1. **Nix / NixOS module** (preferred) — flake output exposes a NixOS module that:
   - installs `lakitu` and `devpod` binaries into `$PATH`
   - runs `loginctl enable-linger <user>`
   - installs the `lakitu-kart@.service` systemd user template
   - ensures `mosh-server` is present
   - ensures the docker daemon is running and the target user is in the `docker` group

2. **Manual install** (documented in README) — download release tarball, copy binaries to `/usr/local/bin`, run `lakitu init`, then follow the printed checklist to cover whatever the Nix module would have automated (linger, systemd unit, mosh-server, docker group).

`lakitu init` is idempotent: it creates `~/.drift/garage/` with a default `config.yaml` if absent, and is safe to re-run. `lakitu new` also creates garage subdirs on demand.

### drift (on each workstation)

Homebrew / Nix / manual binary install. No init command — `~/.config/drift/config.yaml` is created on first `drift circuit add`.

---

## Future

- **Ports management** — `drift ports` subcommands, conflict detection, remapping, standalone forwarding, per-workstation remap persistence.
- **`lakitu serve` streaming RPC** — long-lived stdin/stdout session with JSON-RPC batching and server-initiated notifications (streaming logs, progress updates). Complements the one-shot `lakitu rpc` mode.
- **Cross-circuit sync** — sync characters / tunes / chest across circuits via a plugin system, syncthing, or a git-backed garage.
- **Additional chest backends** — age, 1Password, Vault, SOPS, etc., behind the `ChestBackend` interface.
- **IDE integration** — devpod's `--ide <name>` (VS Code, JetBrains, OpenVSCode, Zed) exposed as a flag on `drift new` / `drift connect`.
- **Auto port detection** — probe running container (`ss -tlnp`) to suggest ports to declare.
- **NixOS module** — packaged install of `lakitu`, systemd template, linger, docker provider.
