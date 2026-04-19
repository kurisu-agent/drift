# drift — command flow breakdown

For each CLI command, traces the path from **client (`drift`)** → **server (`lakitu`)** → **`devpod`**. Companion to [01-original-plan.md](./01-original-plan.md).

**Conventions used below:**
- Every drift→lakitu call is a **JSON-RPC 2.0 request** piped over `ssh <host> lakitu rpc`. Per-command flows show the method name and key params only; the SSH + JSON-RPC plumbing is uniform and documented in [§ RPC protocol](./01-original-plan.md#rpc-protocol).
- **Authentication is out of scope** — whatever makes `ssh <user>@<circuit>` work on the user's machine (keys, agent, CA, jumphost, Tailscale SSH, etc.) is what drift uses. See [§ Transport and authentication](./01-original-plan.md#transport-and-authentication).
- Version check (`server.version` RPC) and config load happen on every drift invocation that contacts a circuit; omitted from per-command flows to reduce noise. See [§ Version compatibility](./01-original-plan.md#version-compatibility).
- "Garage" refers to `~/.drift/garage/` on the circuit.

---

## `drift new <name> [flags]`

**Purpose:** Create a new kart on the circuit. Fails if the name already exists — use `drift start` / `drift connect` to resume an existing kart, or `drift delete` first.

**Client (`drift`):**
1. Validate `<name>` against kart-name regex locally — fail fast before SSH.
2. Enforce `--clone`/`--starter` mutual exclusion.
3. RPC: `kart.new` with params `{name, clone?, starter?, tune?, features?, devcontainer?, character?, autostart?}`.
4. Render the response: print final status on success, or the RPC error on failure.

**Server (`lakitu new`):**
1. Re-validate name; reject with a name-collision error if `garage/karts/<name>/` already exists.
2. Resolve flag composition: server defaults → tune → explicit flags ([§ Flag composition](./01-original-plan.md#flag-composition-and-resolution)).
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
1. RPC: `kart.info` with params `{name}` → result is the kart-info payload ([schema](./01-original-plan.md#lakitu-info-kart--json-schema)).
2. Branch on `status`:
   - `not_found` → error.
   - `stopped` → RPC `kart.start` with params `{name}` (auto-start), then continue.
   - `running` → continue directly.
   - `busy` → brief wait-and-retry on `kart.info`, then treat as `running` when ready; give up with error after a timeout.
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

**Client (`drift`):** RPC `kart.start` with params `{name}`; render response.

**Server (`kart.start` handler):**
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

**Client (`drift`):** RPC `kart.stop` with params `{name}`; render response.

**Server (`kart.stop` handler):**
1. Verify `garage/karts/<kart>/` exists.
2. `devpod stop <kart>`.

**Devpod calls:**
```
devpod stop <kart>
```

---

## `drift restart <kart>`

**Purpose:** Stop then start a kart. Useful after changing environment config (tune or character edits that need a fresh container state) or to recover from a wedged process.

**Client (`drift`):** RPC `kart.restart` with params `{name}`; render response.

**Server (`kart.restart` handler):**
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

**Client (`drift`):** RPC `kart.delete` with params `{name}`.

**Server (`kart.delete` handler):**
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

**Client (`drift`):** RPC `kart.list` with no params; render result array as a table (or pass through with `--output json`).

**Server (`kart.list` handler):**
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

**Client (`drift`):** RPC `kart.enable` or `kart.disable` with params `{name}`.

**Server (`kart.enable` handler):**
1. `touch garage/karts/<kart>/autostart`.
2. `systemctl --user enable --now lakitu-kart@<kart>`.

**Server (`kart.disable` handler):**
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
2. Probe the circuit: RPC `server.version` — warn (but don't fail) if lakitu isn't installed yet, so users can add a circuit pre-bootstrap.

**Server:** none (list), version-only (add probe).
**Devpod calls:** none.

---

## `drift character [list|add|show|rm]`

**Purpose:** Manage git/GitHub identity profiles on the circuit.

**Client (`drift`):** RPC `character.list` / `character.add` / `character.show` / `character.remove`.

**Server handlers:**
- `character.list` — enumerate `garage/characters/*.yaml`, return array of `{name, git_name, git_email, github_user}`.
- `character.add` params `{name, git_name, git_email, github_user?, pat?, ssh_key?}` — validate PAT chest reference via `ChestBackend.List`, validate ssh key file exists, write `garage/characters/<name>.yaml`.
- `character.show` params `{name}` — return the yaml contents (PAT value NOT resolved; `chest:<name>` reference surfaced as-is).
- `character.remove` params `{name}` — delete the yaml. Reject with `code: 4` if any kart references it (scan `garage/karts/*/config.yaml`).

**Devpod calls:** none. (Characters are consumed at `drift new` time.)

---

## `drift chest [set|get|list|rm]`

**Purpose:** Manage secrets on the circuit through the pluggable `ChestBackend`.

**Client (`drift chest set <name>`):**
1. Prompt user for value on stdin (tty, echoing off).
2. RPC `chest.set` with params `{name, value}` — value is a field of the JSON-RPC request body, which itself is piped over the SSH channel's stdin.
3. **Value never appears on argv anywhere** — no shell history, no `ps` listing. The JSON-RPC request travels over the SSH pipe and is consumed by `lakitu rpc`.

**Client (`drift chest get <name>`):** RPC `chest.get` params `{name}` → `result.value`.
**Client (`drift chest list`):** RPC `chest.list` → `result` is array of names.
**Client (`drift chest rm <name>`):** RPC `chest.remove` params `{name}`.

**Server handlers:**
- Dispatch to the configured backend (`config.yaml` → `chest.backend`).
- MVP backend `yamlfile`: reads/writes `garage/chest/secrets.yaml` (0600), top-level `name: value` map, multi-line values via YAML block scalars.

**Devpod calls:** none. (Secrets are resolved and injected into layer-1 dotfiles at `drift new` time.)

---

## `drift version` *(implicit, client-only)*

**Purpose:** Print drift's semver. No server call, no devpod call.

---

## End-to-end example: `drift new kurisu-api --clone git@github.com:kurisu/api.git --tune node --character kurisu --autostart`

```
┌─────────────── workstation ──────────────────────────────┐
│ drift new kurisu-api ...                                 │
│  ├─ validate name regex                                  │
│  ├─ RPC server.version (over: ssh dev@circuit lakitu rpc)│ ← compat check
│  └─ RPC kart.new with params:                            │
│     { "name":"kurisu-api",                               │
│       "clone":"git@github.com:kurisu/api.git",           │
│       "tune":"node", "character":"kurisu",               │
│       "autostart":true }                                 │
└──────────────────┬───────────────────────────────────────┘
                   │  (JSON-RPC request on ssh stdin)
                   ▼
┌─────────────── circuit ──────────────────────────────────┐
│ lakitu rpc → dispatch "kart.new"                         │
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
│  ├─ systemctl --user enable --now                        │
│  │    lakitu-kart@kurisu-api                             │
│  └─ write JSON-RPC response to stdout:                   │
│     { "jsonrpc":"2.0","id":1,                            │
│       "result":{ ...kart.info payload... } }             │
└──────────────────────────────────────────────────────────┘
```
