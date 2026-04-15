# drift — implementation plan

## Overview

Two binaries that manage devpod workspaces server-side, with a thin SSH-based client.

- **`drift`** — client CLI, installed on workstations. Zero local state.
- **`lakitu`** — server daemon, installed on each circuit. Owns all state.

---

## Naming (Mario Kart)

| drift term   | meaning                                              |
|--------------|------------------------------------------------------|
| **kart**     | a devpod workspace / dev container                   |
| **circuit**  | a remote dev server (e.g. `my-server`)               |
| **character**| a git/GitHub identity profile                        |
| **tune**     | a named preset of default flags for kart creation    |
| **boost**    | a mosh session (preferred) or SSH fallback           |
| **garage**   | server-side state dir (`~/.drift/garage/`)           |

---

## CLI design

### `drift` (client)

All commands except `circuit` and `into` delegate to `lakitu` on the circuit via SSH.

```
drift new   <name>  [flags]   — create kart from scratch (no existing repo)
drift clone <url>   [flags]   — create kart from existing repo
drift into  <kart>  [flags]   — connect (mosh → boost, ssh fallback)
drift park  <kart>            — stop kart
drift retire <kart>           — remove kart
drift grid                    — list karts and status
drift enable  <kart>          — auto-start on server reboot
drift disable <kart>          — disable auto-start
drift circuit [list|add]      — manage circuits (client-side config)
drift character [list|add|show|rm]  — manage identity profiles (server-side)
```

Global flags (all commands):
```
--circuit, -c <name>   target circuit (overrides default)
--debug                verbose output
```

#### `drift new` flags

```
--starter <git-url>        template repo; history is discarded after clone
--tune    <name>           named preset (provides defaults for other flags)
--features <json>          devcontainer features JSON, injected via devpod
                           --additional-features (additive, merged last)
--devcontainer <src>       override devcontainer: JSON string, file path, or URL
                           passed as devpod --extra-devcontainer-path
--character <name>         git/github identity to inject
--ports <3000,5432>        ports to forward on connect
--autostart                enable auto-start on server reboot
```

#### `drift clone` flags

Same as `new` minus `--starter`. The positional arg is the repo URL.
Kart name defaults to the repo name (last path segment, sans `.git`).

#### `drift into` flags

```
--ports, -p <3000,5432>   override port forwarding (default: read from kart config)
--no-ports, -N            disable all port forwarding
--ssh                     force plain SSH (skip mosh)
```

#### `drift circuit add` flags

```
--host <user@host>    SSH destination
--default             set as default circuit
```

#### `drift character add` flags

```
--name  <str>         git author name
--email <str>         git author email
--github <str>        github username
--pat   <token>       GitHub PAT (stored at mode 0600 on the server)
--ssh-key <path>      path to SSH private key file on the server
```

---

### `lakitu` (server)

Runs locally on the circuit. Can be invoked directly or via SSH from `drift`.

```
lakitu new    <name>  [flags]   — create kart (same flags as drift new)
lakitu clone  <url>   [flags]   — clone kart (same flags as drift clone)
lakitu park   <kart>            — stop
lakitu retire <kart>            — remove
lakitu grid                     — list
lakitu info   <kart>            — JSON kart info (used by drift into)
lakitu enable  <kart>           — autostart on
lakitu disable <kart>           — autostart off
lakitu logs   <kart>            — systemd journal for kart service
lakitu character [list|add|show|rm]
lakitu tune    [list|show|set|rm]
lakitu config  [show|set]       — server-level config
```

---

## Flag composition and resolution

When creating a kart, flags are resolved in this order (later wins):

```
1. server defaults (lakitu config)
   default_tune, default_character, dotfiles_repo, nix_cache_url

2. tune preset (if --tune specified or default_tune set)
   provides: starter, features, devcontainer

3. explicit flags (--starter, --features, --devcontainer, --character, --ports)
   always override tune values

4. --features is always ADDITIVE — merged on top of whatever devcontainer
   specifies, never replaces. Maps directly to devpod --additional-features.

5. --devcontainer fully overrides any devcontainer.json in the repo.
   Accepts: file path → passed as-is to devpod --extra-devcontainer-path
            JSON string → written to temp file, then passed
            URL → downloaded to temp file, then passed
```

`--tune none` disables all tune defaults. `--tune default` is implicit.

---

## Kart creation modes

```
drift new  myproject                      → starter from default tune (or blank)
drift new  myproject --starter <url>      → scaffold from git url, strip history
drift clone https://github.com/org/repo   → clone repo, name = "repo"
drift clone https://... myname            → clone with explicit name
```

Under the hood both call `devpod up --provider docker --ide none` on the server
with the resolved flags.

---

## Server state layout

```
~/.drift/garage/
  config.json                   server-level defaults
  tunes/
    default.json                { starter?, features?, devcontainer? }
    node.json
    python.json
  characters/
    <name>/
      config                    GIT_NAME, GIT_EMAIL, GITHUB_USER, SSH_KEY_PATH
      token                     GitHub PAT (0600)
  karts/
    <name>/
      config                    REPO, TUNE, CHARACTER, USER, SHELL
      ports                     one port per line
      autostart                 presence = auto-start enabled
```

### `config.json` fields

```json
{
  "default_tune":      "default",
  "default_character": "",
  "dotfiles_repo":     "",
  "nix_cache_url":     ""
}
```

### Tune profile fields (all optional)

```json
{
  "starter":      "https://github.com/org/starter-repo",
  "devcontainer": "https://example.com/devcontainer.json",
  "features":     { "ghcr.io/devcontainers/features/node:1": { "version": "lts" } }
}
```

---

## Client config layout

```
~/.config/drift/config          key=value, shell-style

DEFAULT_CIRCUIT=my-server
CIRCUIT_my_server_HOST=dev@my-server.example.com
CIRCUIT_other_HOST=dev@other.example.com
```

---

## Connection flow (`drift into`)

1. Client SSHes to circuit: `ssh <host> lakitu info <kart>` → JSON
2. Parse container name, ports, user, shell
3. Build port-forward specs from kart ports (or flag overrides)
4. If mosh available → `mosh --ssh "ssh -A <forwards>" <host> -- docker exec -it --user <user> <container> <shell>`
5. Else → `ssh -A -t <forwards> <host> docker exec -it --user <user> <container> <shell>`

SSH agent forwarding (`-A`) always on so character SSH keys propagate into the container.

---

## Auto-start on reboot

Each enabled kart gets a systemd user service:

```
~/.config/systemd/user/lakitu-kart@.service   (template unit)
```

`lakitu enable <kart>` → `systemctl --user enable --now lakitu-kart@<kart>`

Server requires `loginctl enable-linger <user>` (set once via NixOS module).

Service runs: `lakitu new <kart>` — devpod up is idempotent, safe to re-run.

---

## Go project structure

Modelled after devpod's patterns:

```
drift/
  main.go                      entry: cmd.Execute()
  cmd/
    root.go                    NewRootCmd(), BuildRoot(), Execute()
    flags/
      global.go                GlobalFlags struct, SetGlobalFlags()
    new.go                     NewNewCmd(*GlobalFlags)     — struct + RunE
    clone.go                   NewCloneCmd(*GlobalFlags)
    into.go                    NewIntoCmd(*GlobalFlags)
    park.go
    retire.go
    grid.go
    enable.go
    disable.go
    circuit/
      root.go                  NewCircuitCmd()
      add.go
      list.go
    character/
      root.go                  NewCharacterCmd(*GlobalFlags)
      add.go
      list.go
      show.go
      rm.go

lakitu/
  main.go
  cmd/
    root.go
    flags/
      global.go
    new.go
    clone.go
    park.go
    retire.go
    grid.go
    info.go
    enable.go
    disable.go
    logs.go
    character/  (same subcommand tree as drift character, no SSH hop)
    tune/
      root.go
      list.go
      show.go
      set.go
      rm.go
    config/
      root.go
      show.go
      set.go

  pkg/                         shared by both binaries (via Go workspace or single module)
    kart/
      kart.go                  Config, Load, Save, Ports, SetAutostart, List, Remove
    garage/
      tune.go                  TuneProfile, LoadTune, SaveTune, ListTunes
      config.go                ServerConfig, LoadServerConfig, SaveServerConfig
    character/
      character.go             Config, Load, Save, PAT, SavePAT, HasPAT, HasSSHKey, Info, List, Remove
    circuit/
      circuit.go               Config, Load, Save, Resolve, SSHArgs
    boost/
      boost.go                 Opts, Connect, connectMosh, connectSSH
    devpod/
      devpod.go                UpOpts, Up, Down, Delete, Status, ContainerName
    api/
      types.go                 KartInfo, CharacterInfo, CircuitConfig (JSON wire types)
```

Key patterns from devpod to follow:
- Each command is a **struct** with flag fields + `RunE` method (not a closure)
- `NewXxxCmd(globalFlags *flags.GlobalFlags) *cobra.Command` constructor
- `SilenceUsage: true`, `SilenceErrors: true` on root
- Env var inheritance: `DRIFT_` prefix, auto-mapped from flag names
- `pkg/` contains all business logic; `cmd/` is thin wiring only

---

## NixOS integration (future)

Additions to `modules/devpod.nix` (or new `modules/drift.nix`):
- Install `lakitu` binary to PATH
- Install `~/.config/systemd/user/lakitu-kart@.service` template
- `users.users.<user>.linger = true` for persistent user services
- Configure devpod with local docker provider on first run

---

## Out of scope (for now)

- `lakitu serve` daemon mode (systemd oneshot per-kart is sufficient)
- Web UI
- Multi-user circuits
- Workspace sharing between characters
