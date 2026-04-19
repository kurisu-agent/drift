# drift

**Devpod for drifters.**
*Remote devcontainers tuned for life on the move — persistent, agentic, phone-friendly.*

[![Release](https://img.shields.io/github/v/release/kurisu-agent/drift)](https://github.com/kurisu-agent/drift/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![CI](https://github.com/kurisu-agent/drift/actions/workflows/ci.yml/badge.svg)](https://github.com/kurisu-agent/drift/actions)

<!-- TODO: demo GIF / asciinema cast of `drift warmup` → `drift new` → `drift connect` -->

drift is a stateless client for remote devcontainer workspaces. It pairs with
`lakitu`, a server-side binary on each remote host (a *circuit*). Together
they wrap [devpod](https://github.com/skevetter/devpod) over plain SSH.

## Built for nomads

Your laptop is the most replaceable thing you own. drift keeps every
workspace, every secret, and every git identity on hosts *you* control, so
your client can be a ThinkPad today, a borrowed Mac tomorrow, and a phone in
a customs queue the day after.

- **Mosh-first persistent shells.** Your session survives flaky hotel wifi,
  tunnel wifi, switching from cafe wifi to cellular, and closing your laptop
  lid for six hours on a flight. `drift connect` picks mosh when it's there
  and falls back to ssh when it isn't.
- **Client independence.** Every client is thin. Anything that speaks SSH is
  a first-class drift client — macOS, Linux, Termux on Android. One config on
  a fresh device and you're back where you left off.
- **Vibe-code from anywhere.** Standing in a customs queue with an idea?
  `drift new scratch-pad --clone git@github.com:you/playground.git` is one
  command from your phone and you're inside a fresh devcontainer on your
  server.
- **AI scaffolding on mobile.** `drift ai` drops you straight into Claude
  running on your circuit, preloaded with drift's command surface. On a
  phone, voice-type the project you want; on a laptop, just describe it.
  Claude can run drift commands directly to spin the workspace up.

## Concepts

| Term       | Meaning                                                    |
|------------|------------------------------------------------------------|
| circuit    | A remote Linux host running `lakitu`                       |
| kart       | A devcontainer workspace on a circuit                      |
| character  | A git identity profile (name, email, signing key, PAT ref) |
| chest      | Server-side secret store on a circuit                      |
| tune       | Reusable preset bundling features, starter repo, dotfiles  |

## Install

Workstation (mac/linux/termux):

```bash
curl -fsSL https://raw.githubusercontent.com/kurisu-agent/drift/main/scripts/install.sh | sh
```

Installs into `~/.local/bin` (or `/usr/local/bin` when run as root).
`DRIFT_INSTALL_DIR=` overrides the target; `DRIFT_VERSION=v1.2.3` pins a tag.
Later: `drift update` to pull the latest release.

<details>
<summary>Circuit (Linux host) setup</summary>

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

</details>

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
drift ai                     # Claude session on the circuit
```

## IDE integration

`drift.<circuit>.<kart>` is a wildcard SSH alias routed through
`drift ssh-proxy`. Drop it into VS Code Remote-SSH, JetBrains Gateway, `scp`,
`rsync` — anything that speaks SSH. No plugin required.

## Version compatibility

drift and lakitu share a semver version. Per-process, drift probes
`server.version` on each circuit:

| mismatch | behavior       |
|----------|----------------|
| major    | error, abort   |
| minor    | warn to stderr |
| patch    | silent         |

Bypass during upgrades with `drift --skip-version-check …`. The probe also
carries an integer `api` field bumped on breaking wire changes, so a
semver-compatible lakitu speaking an older RPC is still rejected.

## Status

Early / evolving. Interfaces may change between minor versions until `v1.0.0`.

## Contributing

drift is developed in the open but **not accepting pull requests**. Issues for
bug reports and discussion are welcome. Feel free to fork and adapt — the MIT
license puts no restrictions on that.

## License

MIT © クリス.コム
