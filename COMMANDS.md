# drift — command flow breakdown

For each CLI command, traces the path from **client (`drift`)** → **server (`lakitu`)** → **`devpod`**. Companion to [PLAN.md](./PLAN.md).

**Conventions used below:**
- `ssh <host> lakitu ...` — drift always shells out to SSH to reach lakitu. No custom wire protocol.
- Version check and config load happen on every drift invocation that contacts a circuit; omitted from per-command flows to reduce noise. See [PLAN.md § Version compatibility](./PLAN.md#version-compatibility).
- "Garage" refers to `~/.drift/garage/` on the circuit.

---

## `drift new <name> [flags]`

**Purpose:** Create a new kart on the circuit. Fails if the name already exists — use `drift start` / `drift connect` to resume an existing kart, or `drift delete` first.

**Client (`drift`):**
1. Validate `<name>` against kart-name regex locally — fail fast before SSH.
2. Enforce `--clone`/`--starter` mutual exclusion.
3. `ssh <circuit> lakitu new <name> [serialized flags]`
4. Stream stdout/stderr; exit with lakitu's exit code.

**Server (`lakitu new`):**
1. Re-validate name; reject with a name-collision error if `garage/karts/<name>/` already exists.
2. Resolve flag composition: server defaults → tune → explicit flags ([PLAN.md § Flag composition](./PLAN.md#flag-composition-and-resolution)).
3. Resolve chest references (`chest:<name>`) in the attached character via `ChestBackend.Get`.
4. If `--starter <url>`: clone to tmpdir, `rm -rf .git`, `git init`, initial commit authored by character.
5. Build layer-1 dotfiles scratch dir from character (`.gitconfig`, `~/.config/gh/hosts.yml`, SSH key, credential helper).
6. Write `garage/karts/<name>/config.yaml` (resolved source, tune, character, source_mode).
7. Run `devpod up` (see below).
8. Apply layer-1 dotfiles into the running container.
9. If `--autostart`: `touch garage/karts/<name>/autostart` + `systemctl --user enable --now lakitu-kart@<name>`.
10. Emit final status JSON on stdout.

**Devpod calls:**
```
devpod up \
  --provider docker \
  --ide none \
  [--additional-features '<features-json>'] \
  [--extra-devcontainer-path <tmpfile>] \
  [--dotfiles <tune.dotfiles_repo>]              # layer 2
  [--devcontainer-image <image>] \
  [--fallback-image <image>] \
  [--git-clone-strategy <strategy>] \
  <source>                                       # tmpdir for starter, url for clone, workspace name for blank
```
Post-`up`:
```
devpod agent workspace install-dotfiles --dotfiles file://<layer-1-scratch-dir>
devpod status <name> --output json              # verify running, included in final output
```

---

## `drift connect <kart> [flags]`

**Purpose:** Attach an interactive terminal to a kart. Auto-starts the kart if it is stopped — users never have to manually `start` before `connect`.

**Client (`drift`):**
1. `ssh <circuit> lakitu info <kart>` → JSON ([schema](./PLAN.md#lakitu-info-kart--json-schema)).
2. Branch on `status`:
   - `not_found` → error.
   - `stopped` → `ssh <circuit> lakitu start <kart>` (auto-start), stream its output, then continue.
   - `running` → continue directly.
   - `busy` → brief wait-and-retry on `lakitu info`, then treat as `running` when ready; give up with error after a timeout.
3. Choose transport:
   - Default: `mosh <circuit> -- devpod ssh <kart>`
   - `--ssh`: `ssh -t <circuit> "devpod ssh <kart>"`
   - `--forward-agent`: add `-A` to the ssh/mosh leg.
4. Exec into the chosen command; drift's own process exits when the session ends.

**Server (`lakitu info`):**
1. Read `garage/karts/<kart>/config.yaml`.
2. `devpod status <kart> --output json` to get live status + container metadata.
3. Merge and emit the JSON schema; exit non-zero with `{"error": ...}` if kart not found.

**Server (`lakitu start`, when invoked for auto-start):** see [`drift start`](#drift-start-kart) below.

**Devpod calls (on circuit):**
```
devpod status <kart> --output json              # via lakitu info
devpod up <kart>                                # only if stopped (auto-start path)
devpod ssh <kart>                               # interactive shell inside container
```

No subprocess port-forwarding yet — ports phase is deferred.

---

## `drift start <kart>`

**Purpose:** Start a stopped kart without re-applying flag composition. Idempotent — a no-op if already running.

**Client (`drift`):** `ssh <circuit> lakitu start <kart>`, stream output.

**Server (`lakitu start`):**
1. Verify `garage/karts/<kart>/` exists — error if not.
2. `devpod up <kart>` (idempotent: starts existing workspace or returns success if already running).
3. Emit status JSON.

**Devpod calls:**
```
devpod up <kart>                                # no flags — existing workspace
devpod status <kart> --output json
```

---

## `drift stop <kart>`

**Purpose:** Stop a running kart (preserve state, don't delete). Idempotent.

**Client (`drift`):** `ssh <circuit> lakitu stop <kart>`, stream output.

**Server (`lakitu stop`):**
1. Verify `garage/karts/<kart>/` exists.
2. `devpod stop <kart>`.

**Devpod calls:**
```
devpod stop <kart>
```

---

## `drift restart <kart>`

**Purpose:** Stop then start a kart. Useful after changing environment config (tune or character edits that need a fresh container state) or to recover from a wedged process.

**Client (`drift`):** `ssh <circuit> lakitu restart <kart>`, stream output.

**Server (`lakitu restart`):**
1. Verify `garage/karts/<kart>/` exists.
2. `devpod stop <kart>` (swallow "already stopped" as success).
3. `devpod up <kart>`.
4. Emit status JSON.

**Devpod calls:**
```
devpod stop <kart>
devpod up <kart>
devpod status <kart> --output json
```

---

## `drift delete <kart>`

**Purpose:** Permanently remove a kart.

**Client (`drift`):** `ssh <circuit> lakitu delete <kart>`.

**Server (`lakitu delete`):**
1. If `garage/karts/<kart>/autostart` exists: `systemctl --user disable --now lakitu-kart@<kart>`.
2. `devpod delete --force <kart>`.
3. `rm -rf garage/karts/<kart>/`.

**Devpod calls:**
```
devpod delete --force <kart>
```

---

## `drift list`

**Purpose:** Show all karts on the circuit with their status.

**Client (`drift`):** `ssh <circuit> lakitu list`, render the returned JSON as a table (or pass through with `--output json`).

**Server (`lakitu list`):**
1. `devpod list --output json` to get live workspace set.
2. For each, overlay garage metadata: tune, character, autostart presence, source_mode.
3. Emit combined JSON array.

**Devpod calls:**
```
devpod list --output json
```

---

## `drift enable <kart>` / `drift disable <kart>`

**Purpose:** Toggle auto-start on circuit reboot.

**Client (`drift`):** `ssh <circuit> lakitu enable|disable <kart>`.

**Server (`lakitu enable`):**
1. `touch garage/karts/<kart>/autostart`.
2. `systemctl --user enable --now lakitu-kart@<kart>`.

**Server (`lakitu disable`):**
1. `rm -f garage/karts/<kart>/autostart`.
2. `systemctl --user disable --now lakitu-kart@<kart>`.

**Devpod calls:** none. (The systemd unit calls `lakitu start <kart>` on boot, which calls devpod — see `drift start` above.)

---

## `drift circuit [list|add]`

**Purpose:** Manage client-side circuit config. Never touches the server for list; add probes the server optionally.

**Client (`drift circuit list`):**
1. Read `~/.config/drift/config.yaml`.
2. Print table: name, host, default marker.

**Client (`drift circuit add --host <user@host> [--default]`):**
1. Write/update `~/.config/drift/config.yaml`.
2. Probe the circuit: `ssh <host> lakitu version` — warn (but don't fail) if lakitu isn't installed yet, so users can add a circuit pre-bootstrap.

**Server:** none (list), version-only (add probe).
**Devpod calls:** none.

---

## `drift character [list|add|show|rm]`

**Purpose:** Manage git/GitHub identity profiles on the circuit.

**Client (`drift`):** `ssh <circuit> lakitu character <subcommand> [args]`.

**Server (`lakitu character`):**
- `list` — enumerate `garage/characters/*.yaml`, emit names + git_name/email summary.
- `add <name> --name ... --email ... [--github ...] [--pat chest:<secret>] [--ssh-key <path>]` — validate PAT reference exists via `ChestBackend.List`, validate ssh key file exists, write `garage/characters/<name>.yaml`.
- `show <name>` — print the yaml (PAT value is NOT resolved; shown as `chest:<name>` reference).
- `rm <name>` — delete the yaml. Reject if any kart references it (scan `garage/karts/*/config.yaml`).

**Devpod calls:** none. (Characters are consumed at `drift new` time.)

---

## `drift chest [set|get|list|rm]`

**Purpose:** Manage secrets on the circuit through the pluggable `ChestBackend`.

**Client (`drift chest set <name>`):**
1. Prompt user for value on stdin (tty, echoing off).
2. `ssh <circuit> lakitu chest set <name>` — pipe the value into lakitu's stdin.
3. **Value never appears on argv anywhere** — no shell history, no ps listing.

**Client (`drift chest get <name>`):** `ssh <circuit> lakitu chest get <name>` → stdout.
**Client (`drift chest list`):** `ssh <circuit> lakitu chest list` → names only.
**Client (`drift chest rm <name>`):** `ssh <circuit> lakitu chest rm <name>`.

**Server (`lakitu chest`):**
- Dispatch to the configured backend (`config.yaml` → `chest.backend`).
- MVP backend `envfile`: reads/writes `garage/chest/secrets.env` (0600), one `KEY=value` per line, shell-quoted values.

**Devpod calls:** none. (Secrets are resolved and injected into layer-1 dotfiles at `drift new` time.)

---

## `drift version` *(implicit, client-only)*

**Purpose:** Print drift's semver. No server call, no devpod call.

---

## End-to-end example: `drift new kurisu-api --clone git@github.com:kurisu/api.git --tune node --character kurisu --autostart`

```
┌─────────────── workstation ──────────────┐
│ drift new kurisu-api ...                 │
│  ├─ validate name regex                  │
│  ├─ ssh dev@circuit lakitu version       │ ← check compat
│  └─ ssh dev@circuit lakitu new \         │
│       kurisu-api --clone ... --tune ...  │
└──────────────────┬───────────────────────┘
                   │
                   ▼
┌─────────────── circuit ──────────────────────────────────┐
│ lakitu new kurisu-api                                    │
│  ├─ name collision check                                 │
│  ├─ resolve flags: server → tune(node) → explicit        │
│  ├─ resolve PAT: ChestBackend.Get("github-pat")          │
│  ├─ build layer-1 dotfiles scratch dir                   │
│  │   (gitconfig, gh auth, credential helper)             │
│  ├─ write garage/karts/kurisu-api/config.yaml            │
│  └─ spawn devpod →                                       │
│      devpod up --provider docker --ide none \            │
│        --additional-features '{node:...}' \              │
│        --extra-devcontainer-path /tmp/dc.json \          │
│        --dotfiles https://github.com/org/dotfiles \      │
│        git@github.com:kurisu/api.git                     │
│      devpod agent workspace install-dotfiles \           │
│        --dotfiles file:///tmp/drift-layer1-XXX           │
│      devpod status kurisu-api --output json              │
│  ├─ touch garage/karts/kurisu-api/autostart              │
│  └─ systemctl --user enable --now                        │
│       lakitu-kart@kurisu-api                             │
└──────────────────────────────────────────────────────────┘
```
