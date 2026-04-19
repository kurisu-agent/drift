# drift

Stateless client for remote devcontainer workspaces. Pairs with `lakitu`, a
server-side binary on each remote host (a *circuit*). Together they wrap
[devpod](https://github.com/skevetter/devpod) over plain SSH.

- **No client state.** Workstations hold only circuit config. Workspaces
  (*karts*) and secrets live on the circuit.
- **SSH-native.** Every RPC is JSON-RPC 2.0 over `ssh`. No daemon, no ports,
  no custom auth.
- **Identities and secrets.** Git-identity profiles (*characters*) and a
  server-side secret store (*chest*).
- **Persistent shells.** `drift connect` prefers mosh, falls back to ssh.
- **Reusable presets.** *Tune* profiles bundle features, starter repos, and
  dotfiles behind one flag.

## Install

Workstation (mac/linux/termux):

```bash
curl -fsSL https://raw.githubusercontent.com/kurisu-agent/drift/main/scripts/install.sh | sh
```

Installs into `~/.local/bin` (or `/usr/local/bin` when run as root). `DRIFT_INSTALL_DIR=` overrides the target; `DRIFT_VERSION=v1.2.3` pins a tag. Later: `drift update` to pull the latest release.

Circuit (Linux host):

```bash
curl -fsSL https://github.com/kurisu-agent/drift/releases/latest/download/lakitu_linux_amd64.tar.gz \
  | sudo tar -xz -C /usr/local/bin lakitu
curl -L -o devpod https://github.com/skevetter/devpod/releases/latest/download/devpod-linux-amd64
sudo install -m 0755 devpod /usr/local/bin/devpod
sudo usermod -aG docker "$USER"
sudo apt-get install -y mosh                    # optional
sudo loginctl enable-linger "$USER"             # systemd user units
mkdir -p ~/.config/systemd/user
curl -fsSL https://raw.githubusercontent.com/kurisu-agent/drift/main/packaging/systemd/lakitu-kart@.service \
  > ~/.config/systemd/user/lakitu-kart@.service
lakitu init
```

## Quickstart

```bash
drift warmup                                    # register a circuit + character
drift new myproj --clone git@github.com:u/myproj.git --character kurisu
drift connect myproj
```

`warmup` is an interactive wizard (circuit SSH target, managed
`~/.config/drift/ssh_config`, git identity, optional PAT into the chest). It's
re-runnable; each phase has a `--skip-*` flag. Non-TTY stdin exits
`code:2 user_error` — script with `drift circuit add`, `drift character add`,
`drift chest set` instead.

## Commands

```text
drift list                   # karts + status
drift start|stop|restart|delete <name>
drift logs <name>
drift enable|disable <name>  # auto-start on circuit reboot
drift circuit    [list|add|rm]
drift character  [list|add|show|rm]
drift chest      [set|get|list|rm]
```

IDEs: `drift.<circuit>.<kart>` is a wildcard SSH alias routed through
`drift ssh-proxy`. Drop it into VS Code Remote-SSH, JetBrains Gateway, `scp`,
`rsync` — anything that speaks SSH. No plugin required.

## Version compatibility

drift and lakitu share a semver version. Per-process, drift probes
`server.version` on each circuit:

| mismatch | behavior          |
|----------|-------------------|
| major    | error, abort      |
| minor    | warn to stderr    |
| patch    | silent            |

Bypass during upgrades with `drift --skip-version-check …`. The probe also
carries an integer `api` field bumped on breaking wire changes, so a
semver-compatible lakitu speaking an older RPC is still rejected.
