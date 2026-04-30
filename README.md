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

drift is deliberately opinionated: **Docker is the only supported devpod provider**. Every convenience in drift (auto-start on boot, `DEVPOD_HOME` isolation, kart-provisioning shortcuts, the Termux path) assumes a local Docker daemon on the circuit. If you need a different runtime, you want plain devpod, not drift.

## Highlights

- **Any client, any circuit, same karts.** State, keys, and secrets live on the server, not on whichever client created the kart, so your desktop, laptop, and phone all see the same karts on the same circuit without wrangling credentials on every device.
- **AI at the CLI.** `drift ai` drops you into Claude on the circuit with drift's command surface preloaded. `drift skill <name>` invokes one of the circuit's Claude skills directly. Long commands are painful to type on a phone, easy to dictate.
- **Persistent shells by default.** `drift connect` uses mosh so sessions survive wifi drops and closing the lid. Falls back to ssh when mosh isn't available.
- **One-flag workspaces.** Preset environments (`tunes`) bundle features, starter repos, and dotfiles, so `drift new myproj --tune <name>` produces a container with the comforts you expect.
- **Secrets that stay on the server.** The `chest` on the circuit holds your SSH keys and PATs; karts read them at start. A borrowed phone never needs to carry them.

## Install

You need a Linux host you can SSH to, with Docker. The client side runs on macOS, Linux, or Termux on Android.

On the host (circuit), run [scripts/install-lakitu.sh](scripts/install-lakitu.sh):

```bash
curl -fsSL https://raw.githubusercontent.com/kurisu-agent/drift/main/scripts/install-lakitu.sh | sh
```

On the client, run [scripts/install-drift.sh](scripts/install-drift.sh):

```bash
curl -fsSL https://raw.githubusercontent.com/kurisu-agent/drift/main/scripts/install-drift.sh | sh
```

On NixOS circuits, import the flake module:

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

Installs lakitu, devpod, and mosh; registers the `lakitu-kart@` user-service template for `drift kart enable`. Package pins are overridable via `services.lakitu.{package,devpodPackage,moshPackage}`.

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
drift start|stop|delete [<name>]    # lifecycle shortcuts; bare drops into the cross-circuit picker
drift kart start|stop|delete [<name>]   # same picker fallback under the namespace form
drift kart info|restart|recreate|rebuild|logs|enable|disable <name>
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
