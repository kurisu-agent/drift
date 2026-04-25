# drift ports — workstation-side forward management

## Problem

Drift passes `forwardPorts` from the devcontainer spec straight through to devpod, which lets ssh handle the forwards as part of the user's shell session. That works on plain ssh. It falls down everywhere else that matters:

1. **Mosh can't carry TCP forwards.** When `drift connect` chooses mosh (the default whenever mosh is on PATH and `--ssh` isn't forced), the user's shell goes over mosh-protocol, but the initial ssh that bootstraps mosh-server exits as soon as mosh-server is up — taking any forwards with it. Today the user gets cryptic `use of closed network connection` errors and has to either re-run with `--ssh` or manually `ssh -L` alongside. TODO.md items 16 and 17 already name this gap.
2. **Forwards are tied to the shell session.** Closing the shell closes the forwards. A browser tab pointed at `localhost:3000` goes dead the moment the user types `exit`. Reopening means starting another `drift connect`, and re-`drift connect` only re-establishes the forwards listed in the devcontainer spec — anything the user wanted to add ad-hoc has to be done again.
3. **No conflict handling.** If `:3000` is already taken on the workstation when devpod tries to bind it, devpod fails the forward silently. The user sees a missing port and goes hunting for it. There's no remap (`use :3001 instead`), no persistence of the remap across sessions, no view of "what forwards do I have right now."
4. **No auto-detection.** A dev server starts inside the kart on a port the devcontainer spec didn't declare, and nothing notices. The user has to know the port, edit `forwardPorts`, rebuild — or open another shell and forward by hand.

The shape of the fix has been on TODO.md for a while: `drift ports` (item 3), auto-detect (item 8), mosh sidecar (item 16), mosh opt-in interim (item 17). This plan resolves all four with one coherent surface.

## Goals

1. Forwards survive the user's shell. Closing `drift connect` does not close the forwards; the next `drift connect` (or an explicit `drift ports`) replays them.
2. Mosh works the same as ssh. Forwards are carried by a separate ssh ControlMaster, not the user's shell session, so the transport choice for the shell is independent.
3. One `drift ports` surface across every circuit and kart on the workstation: TUI for browse / live status, scriptable subcommands for add / rm / remap.
4. Conflict handling is explicit and persistent. If `:3000` is taken locally, drift suggests a remap (`:3001`), the user accepts once, and the remap survives across `drift connect` invocations and workstation reboots.
5. No long-lived drift-owned background process. Persistence comes from a state file plus ssh ControlMasters that drift owns the lifecycle of — not from a `driftd` daemon. Termux and macOS and Linux behave the same way.

## Non-goals

- **Container-side daemon.** Auto-detection runs by polling `ss -tlnp` inside the kart over the existing master, not by installing an agent inside the container. If a user wants a push-based "this port just opened" notification they can wire it into their tune; not drift's job.
- **Forwards for non-localhost destinations.** `drift ports add 3000` forwards `localhost:3000` on the workstation to `localhost:3000` inside the kart. No remote-side bind, no `0.0.0.0`, no host-to-host forwards across karts. If you need that, `ssh -L` by hand.
- **Reverse forwards** (`-R`). Same reasoning — niche, scriptable by hand, easy to add later if it turns out to be common.
- **Cross-kart sharing of a local port.** If two karts both want `:3000` forwarded, one of them gets remapped. No magic round-robin.
- **Stable per-port DNS names** (`web.alpha.drift.local`). A future plan can layer an mDNS / hosts-file shim on top of the same state file; not this plan.
- **Forwards persisting across workstations.** State is local to one workstation (`~/.config/drift/ports.yaml`). If you want the same remaps on a second laptop, copy the file. No sync.
- **Restart of the dev server inside the kart.** The container's process tree is untouched by anything in this plan. Forwards going up or down only affects the workstation-side listener.

## Architecture

### Lifecycle model

The "sidecar" is a discipline, not a process. Two pieces:

1. **A state file**, `~/.config/drift/ports.yaml`, owned by drift, listing every forward the user has asked to keep alive.
2. **One ssh ControlMaster per kart that has any wanted forwards**, owned by drift's lifecycle (drift starts it when forwards exist, drift kills it when the last forward is removed).

Drift never spawns a custom long-lived process. The masters are plain `ssh -M -N -f drift.<circuit>.<kart>` — Android / launchd / systemd treat them as ordinary ssh, not drift state. If the OS reaps a master, the next `drift ports` / `drift connect` reopens it from the state file.

The rule: **a master exists for `drift.<circuit>.<kart>` iff `ports.yaml` lists at least one forward for that kart.** Drift's reconcile loop (synchronous, runs only when invoked) walks the state file:

```
for kart in ports.yaml.forwards:
    if not ssh -O check drift.<circuit>.<kart>:
        ssh -M -N -f drift.<circuit>.<kart>
    for fwd in kart.forwards:
        if not in ssh forward list (tracked client-side):
            ssh -O forward -L <local>:localhost:<remote> drift.<circuit>.<kart>
for kart not in ports.yaml.forwards:
    if ssh -O check ...:
        ssh -O exit ...
```

`ssh -O check` is fast (microseconds; just a control-socket ping). The reconcile is cheap enough to run on every `drift connect` and every `drift ports` invocation.

### ssh_config integration

Drift already manages `~/.config/drift/ssh_config` (see `internal/sshconf/sshconf.go`). Each circuit block already has:

```
ControlMaster auto
ControlPath ~/.config/drift/sockets/cm-%r@%h:%p
ControlPersist 10m
```

The wildcard block (`Host drift.*.*`) has the same plus a `ProxyCommand drift ssh-proxy %h %p` that routes through the circuit to the kart's interior.

This plan touches the config minimally:

- **No change to ControlPersist.** It stays 10m. Persist only governs what happens when the master goes idle (no shell, no forward, no bound channel). Drift's reconcile keeps masters alive while forwards are wanted, and explicitly kills them when not. Persist's 10m is the cleanup window for the "drift forgot to kill, user walked away" case.
- **No new directives.** Forwards are managed at runtime via `ssh -O forward` / `-O cancel`, which never touches the file.

The user's `~/.ssh/config` is unchanged. The single `Include` line drift wrote at install time is enough.

### State file

`~/.config/drift/ports.yaml`:

```yaml
version: 1
forwards:
  alpha/web:                       # <circuit>/<kart>
    - {local: 3000, remote: 3000}
    - {local: 5433, remote: 5432, remapped_from: 5432}
  alpha/api:
    - {local: 8080, remote: 8080}
  beta/scratch:
    - {local: 9229, remote: 9229}
```

Atomic writes via the existing `internal/config.WriteFileAtomic`. The schema lives next to the other client-side config types in `internal/config/`.

### Forward sources

Three ways a forward gets into the state file:

1. **Explicit user action.** `drift ports add 3000` (and friends).
2. **Devcontainer spec passthrough.** On `drift connect`, drift reads the kart's `forwardPorts` from the resolved devcontainer config (already available via the `kart.connect` RPC payload) and unions them into the state file with `source: devcontainer`. Removing the port from the devcontainer spec on the next connect prunes the entry — devcontainer-source forwards are reconciled to match.
3. **Auto-detect (opt-in).** Inside the bubbletea TUI, an "auto" toggle starts a periodic `ss -tlnp` poll inside the kart (via the existing master, no extra connection). Newly-listening ports appear in a "detected" pane; the user accepts with a keystroke, which writes them to the state file with `source: auto`.

User-added forwards take precedence over devcontainer ones — if the user explicitly remaps `5432 → 5433`, a future devcontainer spec that lists `5432` doesn't clobber the remap.

### Conflict handling

When a forward is added (any source) and the chosen local port is already bound, drift picks the next free port and records `remapped_from`. The CLI prints the remap; the TUI shows it inline. The remap is sticky: subsequent reconciles use the remapped local port, even after the original conflict goes away, until the user explicitly clears it with `drift ports remap 5432:5432` (or `drift ports rm 5432`).

Probing for "is this port free" uses a non-blocking bind on `127.0.0.1:<port>` and immediate close. No external dependency.

### CLI surface

```
drift ports                          # bubbletea TUI, all circuits + karts
drift ports list [--kart <c>/<k>]    # scriptable table
drift ports add <port> [--kart …]    # add forward; remaps on conflict
drift ports rm <port>  [--kart …]    # remove forward, kill master if last
drift ports remap <remote>:<local>   # change local port for a remote
drift ports up [--kart …]            # reconcile state file → live
drift ports down [--kart …]          # cancel forwards, kill master(s)
drift ports status                   # JSON, for scripting
```

Without `--kart`, `add` / `rm` / `remap` infer the kart from CWD (same kart-context discovery `drift run` already uses), or prompt via `huh.Select` if ambiguous.

### TUI

Bubbletea panel layout:

```
┌─────────────────────────────────────────────────────────┐
│ drift ports                                       [q]uit│
├─────────────────────────────────────────────────────────┤
│ alpha/web         master: live (uptime 14m)            │
│   3000  →  localhost:3000              source: explicit │
│   5433  →  localhost:5432 (remapped)   source: explicit │
│                                                         │
│ alpha/api         master: live (uptime 4h)             │
│   8080  →  localhost:8080              source: devc     │
│                                                         │
│ beta/scratch      master: down                         │
│   9229  →  localhost:9229              source: explicit │
│                                                         │
│ DETECTED (auto-poll every 5s)                          │
│   alpha/web   :4000  [a]ccept  [i]gnore                │
└─────────────────────────────────────────────────────────┘
[a] add  [d] remove  [r] remap  [u] up  [s] stop  [w] watch
```

Bubbletea is new to drift (the existing TUI bits are `huh` / `lipgloss` only). Adding it is one dependency, used in one place; not a slippery slope. State updates via a 1s ticker that re-reads the state file + `ssh -O check` for each kart.

### Mosh interop

Forwards live on a master ssh that drift opens specifically for the kart in question (`ssh -M -N -f drift.<circuit>.<kart>`). The user's mosh shell session is a separate connection entirely. Closing the mosh session does not affect the master. The master sees no shell, just forwards, and stays alive as long as drift's reconcile says it should.

This is the answer to TODO #16 (mosh sidecar) and TODO #17 (mosh opt-in interim) — both go away as soon as `drift ports up` is wired into `drift connect`'s pre-exec hook.

### `drift connect` integration

In `internal/cli/drift/connect.go`'s `doConnect`, after the `kart.connect` RPC returns and before `connect.Exec()`:

1. Union the kart's resolved `forwardPorts` into `ports.yaml` (devcontainer source).
2. Run reconcile for that kart only.
3. Exec into the user's shell as today.

The user sees a one-line note (`forwards: 3000, 5433 (was 5432)`) on connect, not a wall of text. Failure to bring up a forward is warned, not fatal — the shell still launches.

### Termux

Everything works the same on Termux. ControlMaster works there; `ssh -M -N -f` works there. State file path falls back to `$XDG_CONFIG_HOME/drift/ports.yaml` per the same logic `internal/cli/drift/state.go` already uses for the update-check state.

The Android-OOM concern that motivated rejecting a custom daemon applies symmetrically to ssh masters — Android may reap them too. The difference is recovery: with a daemon, drift would have to respawn it and re-establish state; with masters, drift's reconcile already does that on every invocation, because reconcile is the primary path, not an exception path. "Master is dead" is the same code path as "master never existed."

## UX notes

- `drift ports` with no subcommand opens the TUI. `drift ports list` is the no-TTY scriptable version (table or `--json`).
- Adding a port that's already in the state file is a no-op (idempotent).
- `drift connect --no-forwards` opts out for one session; doesn't edit the state file. `drift ports down --kart …` is the persistent opt-out.
- A forward whose remote port has nothing listening inside the kart still gets set up — ssh doesn't probe, and neither do we. The TUI shows a `[remote idle]` hint so the user knows.
- Conflict remaps print a one-line `remapped 5432 → 5433` notice; not buried in a verbose flag.

## Observability / failure modes

- `drift ports status --json` emits the state file plus per-kart master liveness, suitable for shell prompts / status bars.
- Reconcile errors (master refused to start, `-O forward` failed) surface on the CLI immediately and on the TUI inline; they do not silently disappear into a log.
- Master deaths between invocations are not "errors"; they're the expected path. Reconcile logs them at debug level only.
- `drift doctor` (if it exists by then; otherwise as a follow-up) gains a "ports" section that walks the state file and reports per-forward health.

## New RPCs

None required for v1. The kart's `forwardPorts` is already in the `kart.connect` payload; auto-detect runs `ss -tlnp` over the existing ssh master (no RPC), which is fine because the master is already established whenever auto-detect is running.

A future plan that wants server-pushed port events (container-side `inotify` on `/proc/net/tcp` or similar) would add RPCs at that point; not now.

## Rollout

Pre-1.0, no compat shims (per CLAUDE.md). Land in one PR:

1. New package `internal/ports/` (state file, reconcile, conflict probe).
2. Wire `drift ports` subcommand surface in `internal/cli/drift/`.
3. Wire `drift connect`'s pre-exec hook into reconcile.
4. Drop TODO.md items 3, 8, 16, 17.

Existing users who never touch `drift ports` see no change in behaviour: their devcontainer-spec forwards still work via the same ssh master path, just owned by drift's reconcile instead of devpod's shell session. Mosh users go from "forwards silently broken" to "forwards work" with no flag flip.

## Test plan

- Unit: state-file round-trip; conflict-probe (bind a port in the test, assert remap); reconcile diff against a fake `ssh -O check` / `-O forward`.
- Integration (`integration/`, `make integration`): full real devcontainer + circuit, run `drift ports add`, observe local listener, `curl localhost:<port>` reaches a process started inside the kart, `drift ports rm`, observe listener gone, master gone.
- Mosh path: same integration but with mosh on PATH; assert that closing the mosh shell does not close the forward, and that re-running `drift connect` finds the master already live.
- Termux: not in CI (no Termux runner today), but the test-plan matrix item is "no `os.Executable` derefs in the new code, no hardcoded `/etc` writes" — the existing Termux trap-list (CLAUDE.md) applies.

## Out of scope / follow-ups

- Reverse forwards (`-R`).
- Cross-kart shared local ports (round-robin / load-balanced).
- mDNS / hosts-file integration for stable per-port hostnames.
- Sync of `ports.yaml` across workstations (would pair naturally with the cross-circuit chest sync plan).
- Container-side push-based port events.
- Promoting the bubbletea TUI to a `drift status` super-dashboard that shows karts + forwards + tune drift in one place.
