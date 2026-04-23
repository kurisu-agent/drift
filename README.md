# drift

**devpod for drifters.** a remote control for devpods on your servers.

[![Release](https://img.shields.io/github/v/release/kurisu-agent/drift)](https://github.com/kurisu-agent/drift/releases) [![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE) [![CI](https://github.com/kurisu-agent/drift/actions/workflows/ci.yml/badge.svg)](https://github.com/kurisu-agent/drift/actions)

## Why not just devpod?

[devpod] keeps workspace state on whichever client created it.

Spin up a workspace from your desktop, then try to manage it from your laptop, and the laptop's devpod has no idea that workspace exists: the provider config, the agent credentials, the workspace metadata all live in `~/.devpod/` on the desktop.

Try running devpod from Termux on Android and you're in a whole separate world of hurt; it mostly doesn't run there at all, good luck typing:

```bash
devpod up https://github.com/example-org/myproj.git \
  --provider docker \
  --ide none \
  --dotfiles https://github.com/example-org/dotfiles \
  --additional-features '{"ghcr.io/example-org/devpod-features/devtools:2":{}}' \
  --dotfiles-script-env GITHUB_TOKEN="$GITHUB_TOKEN"
```

drift powerslides past all these problems, letting you drift between devices, servers, and continents with zero friction and ride the drift boost of developer experience out of every corner.

## Highlights

- **Multiple circuits, one client.** Register more than one host and switch between them with `drift -c <name>`. Fly from Osaka to London and the box in your attic is suddenly 200ms away; spin up a kart on a nearer circuit and keep going. `drift status` shows every circuit you've registered side by side along with their karts.
- **AI at the CLI.** `drift ai` drops you into Claude on the circuit with drift's command surface preloaded. `drift skill <name>` invokes one of the circuit's Claude skills directly. Long commands are painful to type on a phone, easy to dictate.
- **Persistent shells by default.** `drift connect` uses mosh so sessions survive wifi drops and closing the lid. Falls back to ssh when mosh isn't available.
- **One-flag workspaces.** Preset environments (`tunes`) bundle features, starter repos, and dotfiles, so `drift new myproj --tune <name>` produces a container with the comforts you expect.
- **Secrets that stay on the server.** The `chest` on the circuit holds your SSH keys and PATs; karts read them at start. A borrowed phone never needs to carry them.

## What you need

A Linux host you can SSH to, with Docker. That's it. The client side runs on macOS, Linux, or Termux on Android.

## Install

On the host (circuit) — [scripts/install-lakitu.sh](scripts/install-lakitu.sh) installs the `lakitu` binary, wires up the systemd user unit + linger, adds you to the `docker` group, optionally installs mosh, and runs `lakitu init`. The pinned devpod binary downloads itself (SHA-verified) on first run.

```bash
curl -fsSL https://raw.githubusercontent.com/kurisu-agent/drift/main/scripts/install-lakitu.sh | sh
```

On the client — [scripts/install-drift.sh](scripts/install-drift.sh) drops the `drift` binary into `~/.local/bin` (or `/usr/local/bin` if run as root, or `$PREFIX/bin` on Termux). `DRIFT_INSTALL_DIR=` overrides the target; `DRIFT_VERSION=v1.2.3` pins a tag. `drift update` pulls newer releases.

```bash
curl -fsSL https://raw.githubusercontent.com/kurisu-agent/drift/main/scripts/install-drift.sh | sh
```

A Nix flake is available for the elite users:

```bash
nix profile install github:kurisu-agent/drift            # client
nix profile install github:kurisu-agent/drift#circuit    # server bundle (includes mosh)
```

On NixOS circuits, import the module instead of wiring lakitu into your host config by hand:

```nix
# flake.nix
{
  inputs.drift.url = "github:kurisu-agent/drift";
  outputs = { self, nixpkgs, drift, ... }: {
    nixosConfigurations.<your-host> = nixpkgs.lib.nixosSystem {
      modules = [ drift.nixosModules.lakitu ./configuration.nix ];
    };
  };
}
```

That one import installs lakitu + devpod + mosh and registers the `lakitu-kart@` user-service template for `drift kart enable`. It deliberately does NOT set `DEVPOD_HOME` globally — lakitu's `kart.connect` RPC returns a fully-resolved remote argv (`env DEVPOD_HOME=… /abs/devpod ssh <kart> …`) that scopes drift's devpod state to the one command, leaving the user's plain `devpod` invocations untouched. Package pins are overridable through `services.lakitu.{package,devpodPackage,moshPackage}` for dev-VM live-tree builds or air-gapped mirrors.

## Quickstart

```bash
drift init                                                   # point at a circuit, set up a git identity
drift new myproj --clone git@github.com:you/myproj.git
drift connect myproj                                         # mosh/ssh into the container
```

`init` is a re-runnable wizard. Each phase has a `--skip-*` flag (`--skip-circuits`, `--skip-characters`, `--no-probe`). Non-TTY stdin exits `code:2 user_error` — script it with `drift circuit add <ssh-target>` first, then `drift init --skip-circuits` on a TTY for the rest.

## Commands

```text
drift init                          # first-time setup wizard
drift status                        # circuits + lakitu health + kart counts
drift update                        # self-install the newest release

drift new <name> [--clone URL|--starter URL] [--tune T] [--character C]
drift connect [<name>]              # merged picker (circuits + karts); mosh (ssh fallback)

drift circuits                      # print configured circuits
drift circuit                       # pick a circuit → shell on its host
drift circuit add|rm|set            # manage circuits (client config + SSH alias)

drift karts                         # print karts across circuits (-c to scope to one)
drift kart                          # pick a kart → connect
drift kart connect [<name>]         # explicit connect
drift kart info|start|stop|restart|delete|logs|enable|disable <name>
drift migrate                       # adopt an existing devpod workspace

drift ai                            # Claude on the circuit, preloaded with drift's CLI
drift skills                        # print available Claude skills
drift skill [<name> [prompt]]       # pick + invoke a skill (explicit name skips picker)

drift runs                          # print ~/.drift/runs.yaml entries
drift run [<name>] [args…]          # pick + execute a run (explicit name skips picker)
```

Global flags: `-c/--circuit <name>`, `-o/--output text|json`, `--no-debug`, `--no-color`. Full per-flag reference: [docs/drift-cli.md](docs/drift-cli.md). Client config shape (default circuit, per-circuit ssh args, etc): [docs/drift-config.md](docs/drift-config.md).

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

`drift.<circuit>.<kart>` is a wildcard SSH alias routed through `drift ssh-proxy`. Drop it into VS Code Remote-SSH, JetBrains Gateway, `scp`, `rsync` — anything that speaks SSH. No plugin required.

## Version compatibility

drift and lakitu share a semver version. Every RPC probes `server.version` first:

| mismatch | behavior       |
|----------|----------------|
| major    | error, abort   |
| minor    | warn to stderr |
| patch    | silent         |

Bypass with `drift --skip-version-check …`. Wire protocol and method catalog: [docs/lakitu-rpc.md](docs/lakitu-rpc.md).

## Status

Early / evolving. Interfaces may change between minor versions until `v1.0.0`.

## Contributing

drift is developed in the open. Issues and pull requests are welcome. Feel free to fork and adapt; the MIT license puts no restrictions on that.

## License

MIT © クリス.コム

[devpod]: https://github.com/skevetter/devpod
