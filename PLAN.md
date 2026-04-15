# drift — implementation plan

## Overview

drift is a Go project — two compiled binaries distributed independently. It wraps [devpod](https://github.com/skevetter/devpod) (maintained fork) to add specific DX quality-of-life features:

- **Server-side management** — workspaces live entirely on remote circuits; clients are stateless
- **Identity and secrets** — first-class git identity profiles (characters) and encrypted secret storage (chest)
- **Persistent connections** — mosh-based sessions that survive network changes
- **Tune profiles** — reusable presets composing devcontainer features, starters, and dotfiles
- **Port management** — conflict detection, remapping, and standalone forwarding
- **Multi-client** — any workstation can connect to any circuit without syncing state

devpod itself is only ever invoked by `lakitu` on the circuit. The `drift` client has no devpod dependency.

---

Two binaries:

- **`drift`** — client CLI, installed on workstations. Zero local state.
- **`lakitu`** — server daemon, installed on each circuit. Owns all state.

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
        │   SSH + port fwd     │                       │
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
│  │   proj-a    │  │   │  │   proj-b   │ │   proj-c   │ │
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
- All kart state, characters, and tunes live in the circuit's garage
- `drift connect` uses mosh to the circuit + `devpod ssh` into the container; port forwarding via devpod's SSH tunnel

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
| **boost**      | a mosh session (SSH fallback)                        |
| **garage**     | server-side state dir (`~/.drift/garage/`)           |
| **chest**      | encrypted secret store on the circuit                |

### Commands

| command              | meaning                                  |
|----------------------|------------------------------------------|
| **`drift new`**      | create a kart (starter or clone)         |
| **`drift connect`**  | connect to a running kart (boost/ssh)    |
| **`drift start`**    | start a stopped kart                     |
| **`drift stop`**     | stop a running kart                      |
| **`drift delete`**   | remove a kart entirely                   |
| **`drift list`**     | list all karts and status                |
| **`drift enable`**   | auto-start kart on server reboot         |
| **`drift disable`**  | disable auto-start                       |
| **`drift circuit`**  | manage remote servers                    |
| **`drift character`**| manage git/GitHub identity profiles      |
| **`drift chest`**    | manage encrypted secrets on the circuit  |

---

## CLI design

### `drift` (client)

All commands except `circuit` and `connect` delegate to `lakitu` on the circuit via SSH.

```
drift new   <name>  [flags]   — create kart (from starter or existing repo)
drift connect  <kart>  [flags]   — connect (mosh → boost, ssh fallback)
drift stop  <kart>            — stop kart
drift delete <kart>           — remove kart
drift list                    — list karts and status
drift enable  <kart>          — auto-start on server reboot
drift disable <kart>          — disable auto-start
drift ports [subcommands]     — configure and manage port forwarding
drift circuit [list|add]            — manage circuits (client-side config)
drift character [list|add|show|rm]  — manage identity profiles (server-side)
drift chest [set|get|list|rm]       — manage encrypted secrets (server-side)
```

Global flags (all commands):
```
--circuit, -c <name>   target circuit (overrides default)
--debug                verbose output
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
--ports <3000,5432>        ports to forward on connect
--autostart                enable auto-start on server reboot
```

`--clone` and `--starter` are mutually exclusive. With neither, defaults from the active tune apply.
Kart name is always the positional `<name>` argument.

#### `drift connect` flags

```
--ports, -p <3000,5432>   override port forwarding (default: read from kart config)
--no-ports, -N            disable all port forwarding
--ssh                     force plain SSH (skip mosh)
--forward-agent           enable SSH agent forwarding (-A) into the circuit
```

#### `drift ports`

Port config (declared ports per kart) lives server-side; forwarding processes run client-side.

Capabilities to expose (exact CLI TBD):
- view/add/remove declared ports for a kart
- start port forwarding standalone, without opening a shell (useful for DB tunnels etc.)
- conflict detection before connecting, with resolution options (kill, remap, skip)
- port remaps persisted locally so `drift connect` applies them automatically


#### `drift circuit add` flags

```
--host <user@host>    SSH destination
--default             set as default circuit
```

#### `drift chest`

Encrypted secret store on the circuit. Secrets never pass through the client — `drift chest set`
SSHes to the circuit and writes the value directly to lakitu's stdin over the SSH pipe.
Encrypted at rest using age, keyed from the server's SSH host key.

```
drift chest set <name>          — prompt for value (never passed as a flag)
drift chest list                — list secret names (values never shown)
drift chest rm   <name>         — remove a secret
```

Secrets are referenced elsewhere as `chest:<name>`:

```sh
drift character add kurisu --pat chest:github-pat
```

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
lakitu new    <name>  [flags]   — create kart (same flags as drift new)
lakitu stop   <kart>            — stop
lakitu delete <kart>            — remove
lakitu list                     — list
lakitu info   <kart>            — JSON kart info (used by drift connect)
lakitu enable  <kart>           — autostart on
lakitu disable <kart>           — autostart off
lakitu logs   <kart>            — systemd journal for kart service
lakitu character [list|add|show|rm]
lakitu tune    [list|show|set|rm]
lakitu ports   [list|add|rm] <kart>    — manage declared ports for a kart
lakitu chest   [set|list|rm]           — manage encrypted secrets
lakitu config  [show|set]              — server-level config
```

---

## Flag composition and resolution

When creating a kart, flags are resolved in this order (later wins):

```
1. server defaults (lakitu config)
   default_tune, default_character, nix_cache_url

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
drift new myproject                          → starter from default tune (or blank)
drift new myproject --starter <url>          → scaffold from git url, strip history
drift new myproject --clone <url>            → clone existing repo
drift new myproject --clone <url> --tune node  → clone + apply tune
```

Under the hood both call `devpod up --provider docker --ide none` on the server
with the resolved flags.

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
    <name>.age                  age-encrypted secret value
  karts/
    <name>/
      config.yaml               repo, tune, character, user, shell
      ports.yaml                list of forwarded ports
      autostart                 presence = auto-start enabled
```

### `config.yaml` fields

```yaml
default_tune: default
default_character: ""
nix_cache_url: ""
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
| connect             | `devpod ssh <name> [-L port:host:port] [--command cmd] [--user user]`     |
| list workspaces     | `devpod list --output json`                                               |
| install dotfiles    | `devpod agent workspace install-dotfiles --dotfiles <url> --env KEY=VAL`  |
| stream logs         | `devpod logs <name>`                                                      |

### Useful `devpod up` flags

```
--provider docker                       use local docker (circuit is host + controller)
--ide none                              no IDE backend — drift manages connections
--additional-features <json>            inject devcontainer features
--extra-devcontainer-path <file>        overlay devcontainer.json (docker provider only)
--dotfiles <url>                        dotfiles repo
--dotfiles-script-env KEY=VALUE         env vars passed to dotfiles install script
--devcontainer-image <image>            override container image
--fallback-image <image>                image when no devcontainer config found
--git-clone-strategy <strategy>         git checkout strategy
--configure-ssh                         update ~/.ssh/config with workspace entry
```

### Useful `devpod ssh` flags

```
-L <local:host:remote>                  forward local port to container
-R <remote:host:local>                  reverse forward
--command <cmd>                         run command instead of interactive shell
--user <user>                           container user
--workdir <path>                        working directory in container
--send-env <VAR>                        forward local env var into container
--set-env KEY=VALUE                     set env var in container
--forward-ports-timeout <duration>      kill session after ports idle
--start-services                        start port forwarding + credential helpers
--ssh-keepalive-interval <duration>     keepalive frequency (default 55s)
```

### Flag mapping (drift → devpod)

| drift / tune field             | devpod flag                                                                                   |
|--------------------------------|-----------------------------------------------------------------------------------------------|
| `--clone <url>` / `--starter`  | positional source arg to `devpod up`                                                          |
| `--features <json>`            | `--additional-features <json>`                                                                |
| `--devcontainer <src>`         | `--extra-devcontainer-path <file>` — JSON string or URL resolved to temp file first           |
| character git name / email     | `--dotfiles-script-env GIT_AUTHOR_NAME=...` / `GIT_COMMITTER_NAME=...` etc.                  |
| tune `dotfiles_repo`           | `--dotfiles <url>`                                                                            |
| character PAT (`chest:<name>`) | PAT decrypted from chest at runtime, embedded in clone URL: `https://<token>@github.com/...` |

### Provider

lakitu configures devpod to use the local `docker` provider — it manages containers directly
on the circuit. The circuit is both the devpod controller and the container host.

### SSH server

devpod injects an SSH server into every container during `devpod up`. `drift connect` uses
`devpod ssh <kart>` on the circuit to reach it — no `docker exec` required. Port forwarding
uses devpod's Go SSH library (`golang.org/x/crypto/ssh`) rather than a separate OS process.

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

devpod injects an SSH server into every container as part of `devpod up`. drift piggybacks on
this — no `docker exec`, no custom SSH server setup needed.

1. Client SSHes to circuit: `ssh <host> lakitu info <kart>` → JSON
2. Resolve ports: flag overrides → kart config → tune defaults

**mosh path (preferred):**

```
 ┌─ mosh <circuit> -- devpod ssh <kart>
 │  (mosh lands on circuit, devpod ssh tunnels into container)
 │  (UDP session — interactive terminal, survives network changes)
 │
 └─ ssh <circuit> "devpod ssh <kart> -L 3000:localhost:3000 -N"
    (port forwarding via devpod's Go SSH library — no subprocess to track)
    (background, killed when mosh exits)
```

mosh does not support port forwarding (closes SSH after UDP handshake).
Port forwarding runs as a separate call alongside the mosh session, using
devpod's own SSH tunnel implementation (`golang.org/x/crypto/ssh`).

**SSH fallback (no mosh on client):**

```
ssh -t <circuit> "devpod ssh <kart> -L 3000:localhost:3000"
```

Single SSH hop to circuit; devpod ssh handles the rest including forwarding.

Agent forwarding (`-A`) is **off by default**. Enable with `--forward-agent` on `drift connect` as an explicit opt-in.

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


## Future

- **IDE integration** — devpod supports VS Code, JetBrains, OpenVSCode, Zed etc. via `--ide <name>`; expose as a flag on `drift new` / `drift connect` when needed
- **Auto port detection** — probe running container (`ss -tlnp`) to suggest ports to add to kart config
- **NixOS module** — install `lakitu`, systemd template unit, enable linger, configure devpod docker provider

