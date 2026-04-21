# drift

**Devpod for drifters.**
*Remote devcontainers tuned for life on the move — persistent, agentic, phone-friendly.*

[![Release](https://img.shields.io/github/v/release/kurisu-agent/drift)](https://github.com/kurisu-agent/drift/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![CI](https://github.com/kurisu-agent/drift/actions/workflows/ci.yml/badge.svg)](https://github.com/kurisu-agent/drift/actions)

<!-- TODO: demo GIF / asciinema cast of `drift init` → `drift new` → `drift connect` -->

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
- **AI scaffolding on mobile.** `drift run ai` drops you straight into
  Claude running on your circuit, preloaded with drift's command surface.
  On a phone, voice-type the project you want; on a laptop, just describe
  it. `drift run scaffolder` goes one step further — hands Claude a
  scaffolding recipe, creates a new kart, and drops you inside it on exit.

## Concepts

| Term       | Meaning                                                    |
|------------|------------------------------------------------------------|
| circuit    | A remote Linux host running `lakitu`                       |
| kart       | A managed devpod on a circuit                              |
| character  | A git identity profile (name, email, signing key, PAT ref) |
| chest      | Server-side secret store on a circuit                      |
| tune       | Reusable preset bundling features, starter repo, dotfiles  |

## Architecture

```text
   Client (your device)                         Circuit (remote Linux host)
   ─────────────────────                        ───────────────────────────

    drift CLI                                    lakitu  (systemd user unit)
      │                                            │
      │  JSON-RPC over SSH                         │  kart.new / list / delete
      ├───────────────────────────────────────────▶│  chest  (secret store)
      │                                            │  runs   (shorthand cmds)
      │  SSH / mosh session                        │
      ├──────────────────────┐                     ▼
      │                      │                   devpod  (docker provider)
      │                      │                     │
    ~/.config/drift/         │                     ▼
      ssh_config  ──────────▶│                   docker daemon
      (managed aliases:      │                     │
       drift.<circ>.<kart>)  │                     ▼
                             └───────────▶  per-kart devcontainer
   IDE / scp / rsync                         ├─ kart "myproj"
   (any SSH tool)                            └─ kart "scratch-pad"
```

`drift` is stateless — every operation is either a JSON-RPC call to `lakitu`
or a direct SSH/mosh session to a kart. Swapping clients is `drift init` on
the new device; no state migrates because none lives there.

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

## Bundled devpod (lakitu)

drift pins a specific devpod build and ships it *inside* the `lakitu`
binary. On first run, lakitu extracts the embedded binary to
`~/.drift/bin/devpod` and invokes it with `DEVPOD_HOME=~/.drift/devpod/`
for every drift-managed workspace. The user's `~/.devpod/` is never
touched — `devpod list` / `devpod delete` from a regular shell keeps
working against whatever devpod the user has installed (if any), fully
isolated from drift's own state tree.

### Why bundle at all

- drift tracks a **fork of devpod** (currently [skevetter/devpod][fork]
  at `v0.22.0`), not the upstream that distro package maintainers are
  likely to ship. Expecting every circuit operator to hand-install the
  right fork is a support-burden trap.
- **Version drift between circuit hosts causes silent, ugly bugs.** A
  circuit running devpod 0.19 and another running 0.22 will diverge in
  workspace JSON shape, flag names, and provider semantics. Embedding
  eliminates the variable.
- **Offline-robust**: bundled binary means no network fetch at server
  bootstrap. Circuits behind NAT, corporate proxies, or air-gapped
  from `github.com` still come up.

### Why content-addressed

Both pins that feed the bundled binary are **SHA256-verified** at build
time — if GitHub ever serves different bytes under the same tag, the
build fails loudly instead of silently shipping a substituted binary.
Supply-chain integrity for the one external dependency drift carries
into every release.

The pin lives in one place: `flake.nix`'s `devpodPin` attribute.

```nix
devpodPin = {
  owner   = "skevetter";
  repo    = "devpod";
  version = "v0.22.0";
  srcHash    = "sha256-MWl+c/IdrizoUMwlMegvJXJ8oerbVw3OPzxHuzMvZSc=";
  vendorHash = "sha256-hCFvOVqtjvbP+pCbAS1LOcFHLFJLkki7DnZmQDr6QFQ=";
};
```

- `srcHash` pins the source tarball from [github.com/skevetter/devpod][fork]
  at tag `v0.22.0`.
- `vendorHash` pins the vendored Go module tree — catches any upstream
  dependency tampering that wouldn't change the tarball hash itself but
  would change what actually gets compiled.

Both are consumed by `pkgs.fetchFromGitHub` (Nix) and by the goreleaser
pre-build hook (for non-Nix release binaries), which re-downloads the
same release asset and verifies its SHA256 before embedding.

### Bumping the pin

Edit `devpodPin.version`, reset both hashes to `"sha256-AAAA…"` (44
A's), and run `nix build .#devpod` twice. Each run fails with the
correct hash in its `got:` line; paste each into the appropriate field.
Same pattern any Nix-packaged Go project uses.

### Non-Nix installs

For manual / distro installs, `lakitu init` still prints its expected
devpod version (from the ldflag) and warns if the on-PATH binary
doesn't match. Once the go:embed work lands, every lakitu release
ships with its pinned devpod inside — the "install devpod manually"
step in the circuit setup goes away.

[fork]: https://github.com/skevetter/devpod

## Quickstart

```bash
drift init                                      # register a circuit + character
drift new myproj --clone git@github.com:u/myproj.git --character kurisu
drift connect myproj
```

Nix users can skip the install script:

```bash
nix profile install github:kurisu-agent/drift            # client
nix profile install github:kurisu-agent/drift#circuit    # server bundle
```

`init` is an interactive wizard (circuit SSH target, managed
`~/.config/drift/ssh_config`, git identity, optional PAT into the chest). It's
re-runnable and each phase has a `--skip-*` flag (`--skip-circuits`,
`--skip-characters`, `--no-probe`). Non-TTY stdin exits `code:2 user_error` —
use `drift circuit add <ssh-target>` first, then run `drift init
--skip-circuits` on a TTY to finish the character phase.

## Commands

```text
drift init                          # first-time setup wizard
drift status                        # circuits + lakitu health + kart counts
drift update                        # self-install the newest release

drift new <name> [--clone URL|--starter URL] [--tune T] [--character C]
drift list                          # karts on the target circuit
drift info <name>                   # one kart's state
drift start|stop|restart <name>     # lifecycle (idempotent)
drift delete <name>                 # remove a kart (errors if missing)
drift enable|disable <name>         # autostart on circuit reboot
drift logs <name>                   # fetch a chunk of kart logs
drift connect <name>                # mosh (ssh fallback); aliases: into, attach
drift migrate                       # adopt an existing devpod workspace

drift circuit list|add|rm|set       # manage circuits (client config + SSH alias)

drift runs                          # list server-side shorthand commands
drift run <name> [args…]            # execute one (built-ins: ai, scaffolder, ping, uptime, …)
```

Global flags: `-c/--circuit <name>`, `-o/--output text|json`, `--no-debug`,
`--no-color`. `drift help --full` prints the Kong-derived catalog including
every lakitu RPC and the exit-code table. See
[docs/drift-cli.md](docs/drift-cli.md) for the per-flag reference of every
subcommand.

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
semver-compatible lakitu speaking an older RPC is still rejected. See
[docs/lakitu-rpc.md](docs/lakitu-rpc.md) for the wire protocol and the
full method catalog.

## Status

Early / evolving. Interfaces may change between minor versions until `v1.0.0`.

## Contributing

drift is developed in the open but **not accepting pull requests**. Issues for
bug reports and discussion are welcome. Feel free to fork and adapt — the MIT
license puts no restrictions on that.

## License

MIT © クリス.コム
