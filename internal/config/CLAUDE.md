# circuit — agent context

You are an AI agent running on a **drift circuit** — a remote dev server. The user launched you via `drift ai` (bare REPL) or `drift skill <name>` (skill dispatch) on their workstation, which mosh/ssh'd in and ran `claude --dangerously-skip-permissions` from this directory (`~/.drift/`). Skills live at `~/.claude/skills/<name>/SKILL.md` on this circuit and are enumerated by `drift skills` from the workstation. (`drift run` is the generic shorthand dispatcher for non-AI commands — `drift runs` lists entries in `~/.drift/runs.yaml`.)

## First thing to do

Run `lakitu help`. It prints the full, up-to-date reference for this circuit's server CLI — every subcommand, every flag, the JSON-RPC method catalog, the on-disk state layout, and the shared exit-code contract. That document is generated from the same source the binary is built from, so it is always accurate. Prefer it over anything you may have cached about drift/lakitu from training data.

## Vocabulary

- **circuit** — this machine.
- **kart** — a devcontainer workspace on this circuit (wraps devpod).
- **character** — a git-identity profile (name, email, SSH key, optional PAT).
- **tune** — a preset: devcontainer features + starter repo + dotfiles.
- **chest** — secrets store (`~/.drift/garage/chest/`, mode 0700).
- **run** — a named shorthand in `~/.drift/runs.yaml` invoked as `drift run <name> [args…]` from the workstation. `drift runs` prints them; bare `drift run` on a TTY picks one.
- **skill** — a Claude Code skill under `~/.claude/skills/<name>/SKILL.md` on this circuit, invoked from the workstation as `drift skill <name> [prompt]`. `drift skills` prints them; bare `drift skill` on a TTY picks one.

## Registering a new run

Edit `~/.drift/runs.yaml` on this circuit. Every entry is one block:

```yaml
runs:
  <name>:
    description: "one-line summary shown in `drift runs`"
    mode: interactive | output
    post: ""                        # optional; connect-last-scaffold is the
                                    # only hook currently known to the client
    args:                           # optional; the client prompts for these
      - name: host                  # when the user runs `drift run <name>`
        prompt: "Host to ping"      # with no positional args. CLI args still
        type: input                 # bypass prompting.
        default: "1.1.1.1"
    command: |
      <shell snippet, expanded server-side>
```

Arg types: `input` (single-line, the default), `text` (multi-line), `select`
(requires a non-empty `options:` list — the `default:` must match one of the
options when both are set). Empty arg values render as `""`; guard with
`{{ if .Arg 0 }}…{{ end }}` when a bare empty positional would break your
command.

Names match `^[a-z][a-z0-9_-]{0,62}$`. Picking the mode:

- **interactive** — the client allocates a TTY and uses mosh when it can. Right for anything that wants a prompt or full-screen UI (shells, editors, `htop`).
- **output** — plain ssh with no pty. Right for request/response things whose stdout the user reads or pipes (`uptime`, `df`, `ping`, one-shot scripts).

Template data available inside `command:` (Go `text/template`):

- `{{ .Arg 0 }}` — Nth positional arg; missing indices render as `""`.
- `{{ .Args }}` — every arg, each single-quoted, joined by spaces.
- `{{ .Arg 0 | shq }}` — POSIX-shell single-quote a single value. Prefer this over bare `{{ .Arg 0 }}` whenever the value could contain whitespace or quotes, i.e. almost always.

For Claude-specific dispatch, prefer skills over runs: add a new SKILL.md under `~/.claude/skills/<name>/` and it becomes reachable as `drift skill <name> [prompt]` from the workstation without touching `runs.yaml`.

After editing, no server restart is needed — the `run.list` / `run.resolve` RPCs re-read the file on every call. Verify with `lakitu help` (which lists the RPC methods) or by asking the user to run `drift runs` on their workstation.

## Adding a new skill

Create `~/.claude/skills/<name>/SKILL.md` on this circuit — the front-matter summary and body become the skill's context when `drift skill <name> [prompt]` dispatches from the workstation. The client pre-prefixes each invocation with "Use the `<name>` skill." so claude picks it up.

## Scope

`drift` is the **client** — it runs on the user's workstation, not here. You will not use `drift` commands on this machine. Everything you can actuate from here is `lakitu …` (local CLI, for inspection and edits that don't go through the wire) or direct filesystem reads under `~/.drift/garage/`. When the user asks about something that only the client can do (`drift init`, `drift new`, `drift connect`, `drift ai`, `drift skill`, `drift run`, …), say so and suggest the command they would run on their workstation.

## Invoking `devpod` directly

When you need to reach devpod below lakitu (inspect a workspace, force-delete a stuck kart, tail an agent log), you must set `DEVPOD_HOME=~/.drift/devpod` — lakitu stores devpod state under `~/.drift/devpod/` rather than the default `~/.devpod/`, so bare `devpod list` will show nothing even when karts exist.

```
DEVPOD_HOME=~/.drift/devpod ~/.drift/bin/devpod list
DEVPOD_HOME=~/.drift/devpod ~/.drift/bin/devpod ssh <kart> --command '…'
DEVPOD_HOME=~/.drift/devpod ~/.drift/bin/devpod delete <kart> --force
```

Use this only for debugging / recovery; routine kart management should go through `lakitu` so the garage and devpod stay in sync.

<!-- drift:user — your notes below this line are preserved across `lakitu init`. Anything above this marker is regenerated from drift's embedded template. -->
