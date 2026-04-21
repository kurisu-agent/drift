# drift

**devpod for drifters.**

[![Release](https://img.shields.io/github/v/release/kurisu-agent/drift)](https://github.com/kurisu-agent/drift/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![CI](https://github.com/kurisu-agent/drift/actions/workflows/ci.yml/badge.svg)](https://github.com/kurisu-agent/drift/actions)

## Why not just devpod?

> drift is a remote control for devpod running on your server.

[devpod] keeps workspace state on whichever client created it. Spin up a
workspace from your desktop, then try to manage it from your laptop,
and the laptop's devpod has no idea that workspace exists: the provider
config, the agent credentials, the workspace metadata all live in
`~/.devpod/` on the first machine. That's the client-state problem. Try
running devpod from Termux on Android and you're in a whole separate
world of hurt; it mostly doesn't run there at all.

drift sidesteps both problems, and provides extra developer experience,
drift boost included.

## Highlights

- **AI at the CLI.** `drift run ai` drops you into Claude on the circuit
  with drift's command surface preloaded. Long commands are painful to
  type on a phone, easy to dictate.
- **Persistent shells by default.** `drift connect` uses mosh so sessions
  survive wifi drops and closing the lid. Falls back to ssh when mosh
  isn't available.
- **One-flag workspaces.** Preset environments (`tunes`) bundle features,
  starter repos, and dotfiles, so `drift new myproj --tune <name>`
  produces a working container without per-project setup.
- **Secrets that stay on the server.** The `chest` on the circuit holds
  your SSH keys and PATs; karts read them at start. A borrowed phone
  never needs to carry them.

## What you need

A Linux host you can SSH to, with Docker. That's it. The client side runs
on macOS, Linux, or Termux on Android.

## Install

On the host (circuit):

```bash
curl -fsSL https://github.com/kurisu-agent/drift/releases/latest/download/lakitu_linux_amd64.tar.gz \
  | sudo tar -xz -C /usr/local/bin lakitu
curl -L -o devpod https://github.com/skevetter/devpod/releases/latest/download/devpod-linux-amd64
sudo install -m 0755 devpod /usr/local/bin/devpod
sudo usermod -aG docker "$USER"
sudo apt-get install -y mosh                    # optional, resilient shells
sudo loginctl enable-linger "$USER"             # systemd user units
mkdir -p ~/.config/systemd/user
curl -fsSL https://raw.githubusercontent.com/kurisu-agent/drift/main/packaging/systemd/lakitu-kart@.service \
  > ~/.config/systemd/user/lakitu-kart@.service
lakitu init
```

On the client:

```bash
curl -fsSL https://raw.githubusercontent.com/kurisu-agent/drift/main/scripts/install.sh | sh
```

Installs into `~/.local/bin` (or `/usr/local/bin` if run as root).
`DRIFT_INSTALL_DIR=` overrides the target; `DRIFT_VERSION=v1.2.3` pins a
tag. `drift update` pulls newer releases.

Nix users can skip both:

```bash
nix profile install github:kurisu-agent/drift            # client
nix profile install github:kurisu-agent/drift#circuit    # server bundle
```

## Quickstart

```bash
drift init                                                   # point at a circuit, set up a git identity
drift new myproj --clone git@github.com:you/myproj.git
drift connect myproj                                         # mosh/ssh into the container
```

`init` is a re-runnable wizard. Each phase has a `--skip-*` flag
(`--skip-circuits`, `--skip-characters`, `--no-probe`). Non-TTY stdin
exits `code:2 user_error` — script it with `drift circuit add
<ssh-target>` first, then `drift init --skip-circuits` on a TTY for the
rest.

## Commands

```text
drift init                          # first-time setup wizard
drift status                        # circuits + lakitu health + kart counts
drift update                        # self-install the newest release

drift new <name> [--clone URL|--starter URL] [--tune T] [--character C]
drift list                          # karts on the target circuit
drift info <name>                   # one kart's state
drift start|stop|restart <name>     # lifecycle (idempotent)
drift delete <name>                 # remove a kart
drift enable|disable <name>         # autostart on circuit reboot
drift logs <name>                   # fetch a chunk of kart logs
drift connect <name>                # mosh (ssh fallback); aliases: into, attach
drift migrate                       # adopt an existing devpod workspace

drift circuit list|add|rm|set       # manage circuits (client config + SSH alias)

drift runs                          # list server-side shorthand commands
drift run ai                        # Claude on the circuit, preloaded with drift's CLI
drift run scaffolder                # Claude with a scaffold recipe; creates a kart on exit
drift run <name> [args…]            # anything else lakitu exposes
```

Global flags: `-c/--circuit <name>`, `-o/--output text|json`,
`--no-debug`, `--no-color`. Full per-flag reference:
[docs/drift-cli.md](docs/drift-cli.md).

## Terms

All of these live server-side, under `~/.drift/garage/` on the circuit.

| Term      | Meaning                                                    |
|-----------|------------------------------------------------------------|
| circuit   | A remote Linux host running `lakitu`                       |
| kart      | A managed devpod workspace on a circuit                    |
| character | A git identity profile (name, email, signing key, PAT ref) |
| chest     | Server-side secret store on a circuit                      |
| tune      | Reusable preset bundling features, starter repo, dotfiles  |

## IDE integration

`drift.<circuit>.<kart>` is a wildcard SSH alias routed through
`drift ssh-proxy`. Drop it into VS Code Remote-SSH, JetBrains Gateway,
`scp`, `rsync` — anything that speaks SSH. No plugin required.

## Version compatibility

drift and lakitu share a semver version. Every RPC probes
`server.version` first:

| mismatch | behavior       |
|----------|----------------|
| major    | error, abort   |
| minor    | warn to stderr |
| patch    | silent         |

Bypass with `drift --skip-version-check …`. Wire protocol and method
catalog: [docs/lakitu-rpc.md](docs/lakitu-rpc.md).

## Status

Early / evolving. Interfaces may change between minor versions until
`v1.0.0`.

## Contributing

drift is developed in the open but **not accepting pull requests**.
Issues for bug reports and discussion are welcome. Feel free to fork and
adapt — the MIT license puts no restrictions on that.

## License

MIT © クリス.コム

[devpod]: https://github.com/skevetter/devpod
