# drift — implementation plan

## Overview

drift is a Go project — two compiled binaries distributed independently. It wraps [devpod](https://github.com/skevetter/devpod) (maintained fork) to add specific DX quality-of-life features:

- **Server-side management** — workspaces live entirely on remote circuits; clients are stateless
- **Identity and secrets** — first-class git identity profiles (characters) and pluggable secret storage (chest)
- **Persistent connections** — mosh-based sessions that survive network changes
- **Tune profiles** — reusable presets composing devcontainer features, starters, and dotfiles
- **Multi-client** — any workstation can connect to any circuit without syncing state

devpod itself is only ever invoked by `lakitu` on the circuit. The `drift` client has no devpod dependency.

---

Two binaries:

- **`drift`** — client CLI, installed on workstations. Zero local state.
- **`lakitu`** — server daemon, installed on each circuit. Owns all state.

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
        │   SSH + mosh         │                       │
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
- `drift connect` uses mosh to the circuit + `devpod ssh` into the container

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

---

## CLI design

### `drift` (client)

All commands except `circuit` and `connect` delegate to `lakitu` on the circuit via SSH.

```
drift new     <name>  [flags]   — create a new kart (from starter or existing repo)
drift connect <kart>  [flags]   — connect (mosh preferred, ssh fallback); auto-starts if stopped
drift start   <kart>            — start a stopped kart
drift stop    <kart>            — stop a running kart
drift restart <kart>            — stop then start
drift delete  <kart>            — remove kart
drift list                      — list karts and status
drift enable  <kart>            — auto-start on server reboot
drift disable <kart>            — disable auto-start
drift circuit   [list|add]          — manage circuits (client-side config)
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

#### `drift new` flags

```
--clone   <git-url>        clone an existing repo (mutually exclusive with --starter)
--starter <git-url>        template repo; history is discarded after clone
--tune    <name>           named preset (provides defaults for other flags)
--features <json>          devcontainer features JSON, injected via devpod
                           --additional-features (additive, merged last)
--devcontainer <src>       override devcontainer: JSON string, file path, or URL
                           passed as devpod --extra-devcontainer-path
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
```

#### `drift chest`

Pluggable secret store on the circuit. **MVP backend is a plain `.env` file** (no encryption) — the goal is to ship the interface and iterate on backends (age, 1Password, Vault, etc.) later without breaking the CLI surface.

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

The active backend is selected in the server's `config.yaml` under the `chest:` key. MVP implements `envfile` (reads/writes `~/.drift/garage/chest/secrets.env`, mode 0600).

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

Runs locally on the circuit. Can be invoked directly or via SSH from `drift`.

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

On each `drift` invocation that contacts a circuit, drift calls `ssh <circuit> lakitu version` (cached per-session) and compares to its own version:

| comparison            | behavior                   |
|-----------------------|----------------------------|
| **major** mismatch    | error, abort               |
| **minor** mismatch    | warning to stderr, continue |
| **patch** mismatch    | silent, continue           |

`--skip-version-check` bypasses the check entirely (needed during upgrades and local testing).

The `lakitu info` JSON schema is versioned by the lakitu semver — additive-only changes within a major.

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
    secrets.env                 MVP backend — plain key=value lines, mode 0600
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
  backend: envfile      # envfile (MVP) | age | onepassword | vault (future)
  # backend-specific config lives under this key
```

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
~/.config/drift/config.yaml

default_circuit: my-server
circuits:
  my-server:
    host: dev@my-server.example.com
  other:
    host: dev@other.example.com
```

---

## Connection flow (`drift connect`)

1. Client runs version check: `ssh <host> lakitu version` → compare to drift version.
2. Client fetches kart info: `ssh <host> lakitu info <kart>` → JSON ([schema](#lakitu-info-kart--json-schema)).

**mosh path (preferred):**

```
mosh <circuit> -- devpod ssh <kart>
  (mosh lands on circuit, devpod ssh tunnels into container)
  (UDP session — interactive terminal, survives network changes)
```

**SSH fallback (no mosh on client):**

```
ssh -t <circuit> "devpod ssh <kart>"
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
- **Cross-circuit sync** — sync characters / tunes / chest across circuits via a plugin system, syncthing, or a git-backed garage.
- **Additional chest backends** — age, 1Password, Vault, SOPS, etc., behind the `ChestBackend` interface.
- **IDE integration** — devpod's `--ide <name>` (VS Code, JetBrains, OpenVSCode, Zed) exposed as a flag on `drift new` / `drift connect`.
- **Auto port detection** — probe running container (`ss -tlnp`) to suggest ports to declare.
- **NixOS module** — packaged install of `lakitu`, systemd template, linger, docker provider.
