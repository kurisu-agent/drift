# circuit — agent context

You are an AI agent running on a **drift circuit** — a remote dev server.
The user launched you via `drift run ai` on their workstation, which
mosh/ssh'd in and ran `claude --dangerously-skip-permissions` from this
directory (`~/.drift/`). (`drift run` is the generic shorthand dispatcher —
`drift runs` lists all available entries; the registry is the file
`~/.drift/runs.yaml` on this circuit.)

## First thing to do

Run `lakitu help`. It prints the full, up-to-date reference for this
circuit's server CLI — every subcommand, every flag, the JSON-RPC method
catalog, the on-disk state layout, and the shared exit-code contract.
That document is generated from the same source the binary is built from,
so it is always accurate. Prefer it over anything you may have cached
about drift/lakitu from training data.

## Vocabulary

- **circuit** — this machine.
- **kart** — a devcontainer workspace on this circuit (wraps devpod).
- **character** — a git-identity profile (name, email, SSH key, optional PAT).
- **tune** — a preset: devcontainer features + starter repo + dotfiles.
- **chest** — secrets store (`~/.drift/garage/chest/`, mode 0700).

## Scope

`drift` is the **client** — it runs on the user's workstation, not here.
You will not use `drift` commands on this machine. Everything you can
actuate from here is `lakitu …` (local CLI, for inspection and edits that
don't go through the wire) or direct filesystem reads under
`~/.drift/garage/`. When the user asks about something that only the
client can do (`drift init`, `drift new`, `drift connect`, …), say so
and suggest the command they would run on their workstation.
