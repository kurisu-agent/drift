# drift

Stateless dev-workstation client for remote devcontainer workspaces. Pairs
with **lakitu**, a server-side binary that lives on each remote **circuit**
(dev server). Together they wrap [devpod](https://github.com/skevetter/devpod)
to deliver a few specific quality-of-life wins:

- **Server-side state.** Workstations hold nothing but circuit config; every
  kart (devpod workspace) and every secret lives on the circuit.
- **SSH-native transport.** Every non-interactive call is JSON-RPC 2.0 over
  a plain `ssh` invocation. No custom daemon, no listening ports, no bespoke
  auth — whatever lets you `ssh` to the circuit is what drift uses.
- **Identity & secrets.** First-class git-identity profiles (**characters**)
  and pluggable secret storage (**chest**).
- **Persistent terminals.** `drift connect` defaults to mosh so your session
  survives network changes; SSH is the fallback.
- **Reusable presets.** **Tune** profiles compose devcontainer features,
  starter repos, and dotfiles into one flag.
- **Multi-client.** Any workstation can connect to any circuit without
  syncing state.

For the full design — architecture, wire protocol, idempotency contracts,
future work — see [`plans/PLAN.md`](plans/PLAN.md). The MVP punch list and
what's landed vs. not lives in [`plans/TODO.md`](plans/TODO.md).

## Quickstart

This walks through the first-time end-to-end flow: install lakitu on a
Linux circuit, install drift on your workstation, register the circuit +
a character, create a kart, and connect.

### 1. Install lakitu on the circuit

Manual-install (the Nix module is the preferred path and will automate
these steps once Phase 17's packaging work lands; for now, everything below
is manual):

```bash
# On the circuit (Linux; Debian/Ubuntu examples)
curl -fsSL https://github.com/kurisu-agent/drift/releases/latest/download/lakitu_linux_amd64.tar.gz \
  | sudo tar -xz -C /usr/local/bin lakitu
sudo chmod +x /usr/local/bin/lakitu

# devpod + docker — lakitu delegates container lifecycle to devpod
curl -L -o devpod "https://github.com/skevetter/devpod/releases/latest/download/devpod-linux-amd64"
sudo install -m 0755 devpod /usr/local/bin/devpod

# docker is required; add your user to the docker group:
sudo usermod -aG docker "$USER"

# mosh (optional but recommended for drift connect):
sudo apt-get install -y mosh

# systemd auto-start support — install the template unit and enable linger
mkdir -p ~/.config/systemd/user
curl -fsSL https://raw.githubusercontent.com/kurisu-agent/drift/main/packaging/systemd/lakitu-kart@.service \
  > ~/.config/systemd/user/lakitu-kart@.service
sudo loginctl enable-linger "$USER"

# One-time garage bootstrap (idempotent):
lakitu init
```

### 2. Install drift on the workstation

```bash
# macOS (Apple silicon example)
curl -fsSL https://github.com/kurisu-agent/drift/releases/latest/download/drift_darwin_arm64.tar.gz \
  | sudo tar -xz -C /usr/local/bin drift

# Linux amd64:
curl -fsSL https://github.com/kurisu-agent/drift/releases/latest/download/drift_linux_amd64.tar.gz \
  | sudo tar -xz -C /usr/local/bin drift
```

Drift has **no** devpod dependency on the workstation. It shells out to
`ssh` (and optionally `mosh`) — nothing else.

### 3. Warm up drift (register a circuit + character)

```bash
drift warmup
```

Interactive wizard that:

1. **Circuits.** Prompts for a circuit name and SSH target (`user@host[:port]`),
   writes a managed `Host drift.<name>` block under `~/.config/drift/ssh_config`
   and ensures `Include ~/.config/drift/ssh_config` exists at the top of
   `~/.ssh/config`. Then probes `server.version` so you know lakitu is
   reachable and compatible.
2. **Characters.** Collect git name, email, optional GitHub username, SSH
   key path, and optional PAT (stored in the chest). Each character
   becomes a git-identity profile you can attach to a kart.
3. **Summary.** Lists what landed and prints a suggested `drift new` command.

Re-runnable. Each phase has a `--skip-*` flag. Non-TTY stdin exits with
`code:2 user_error` — use `drift circuit add`, `drift character add`, and
`drift chest set` directly if you're scripting.

### 4. Create and connect to a kart

```bash
drift new myproject --clone git@github.com:user/myproject.git --character kurisu
drift connect myproject
```

`drift new` creates a devcontainer on the circuit, applies the character's
identity (gitconfig, gh CLI auth, optional SSH key), and registers the
workspace with devpod. `drift connect` auto-starts the kart if stopped,
then opens a mosh session (falling back to `ssh -t`) and drops you into
the container.

### 5. Everything else

```bash
drift list                   # show karts and their status
drift start <name>           # start a stopped kart
drift stop <name>            # stop a running kart
drift restart <name>
drift delete <name>          # errors with code:3 if missing
drift logs <name>            # chunk of kart logs
drift enable <name>          # auto-start on circuit reboot (systemd user unit)
drift disable <name>
drift circuit [list|add|rm]  # manage circuits (client-side)
drift character [list|add|show|rm]
drift chest [set|get|list|rm]  # manage secrets on the circuit
```

IDE integration via the `drift.<circuit>.<kart>` wildcard alias: plug that
host into VS Code Remote-SSH, JetBrains Gateway, `scp`, `rsync`, or
anything else that speaks SSH. No IDE plugin required — OpenSSH's
ProxyCommand routes the session through `drift ssh-proxy` transparently.

## Manual-install checklist (circuit)

What the (future) NixOS module will automate, for now done by hand:

1. `lakitu` binary in `$PATH`.
2. `devpod` binary in `$PATH`.
3. `docker` daemon running; target user in the `docker` group.
4. `mosh-server` installed (optional but enables `drift connect`'s mosh path).
5. `loginctl enable-linger <user>` so systemd user units survive logout.
6. `packaging/systemd/lakitu-kart@.service` installed under
   `~/.config/systemd/user/` so `drift enable <kart>` has a template to
   instantiate.
7. `lakitu init` once per new user — creates `~/.drift/garage/` with its
   standard subdirs and a default `config.yaml`.

## Version compatibility

drift and lakitu share a **semver** version. On every non-local drift
invocation, drift caches a `server.version` probe per circuit per process
and compares:

| mismatch     | behavior              |
|--------------|-----------------------|
| **major**    | error, abort          |
| **minor**    | warning to stderr     |
| **patch**    | silent                |

During upgrades (new drift against old lakitu, or vice versa) you can
bypass the check:

```bash
drift --skip-version-check <subcommand> …
```

Dropping `--skip-version-check` once both sides are in the same major
restores the safety net. The `server.version` response also carries an
integer `api` field bumped only on breaking wire changes, so a lakitu
that's semver-compatible on paper but speaks an older RPC surface is
rejected explicitly.

## Links

- [`plans/PLAN.md`](plans/PLAN.md) — full design spec
- [`plans/COMMANDS.md`](plans/COMMANDS.md) — per-command contracts
- [`plans/TODO.md`](plans/TODO.md) — punch list
