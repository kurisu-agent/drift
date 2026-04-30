# Plan 15 — Ports become session-scoped (and probe sees devcontainer.json)

## Why

Plan 13 shipped the ports machinery with one strong design choice: forwards survive shell sessions. The intent was that `drift connect` could come and go without users losing their carefully-configured local→remote mappings. In practice this had two friction points:

1. **`drift ports probe` is post-start only.** The picker enumerates listening sockets via `ss -tlnpH` inside the kart, so users who just spun up a kart (or whose dev server isn't running yet) see an empty list. Meanwhile the kart's `devcontainer.json` already declares `forwardPorts` — the port set the kart's authors *intended* to expose — and probe can't see it.
2. **Conflicts are silent.** When a devcontainer port is already bound on the workstation, `UnionDevcontainer` quietly remaps it via `PickFreePort` and writes the new mapping to `ports.yaml`. Users only notice when they hit the "wrong" local port. A small confirmation step is much friendlier.
3. **Forwards leak across sessions.** Because they persist past the shell exit, the workstation accumulates bound ports. Two sequential `drift connect` calls to different karts that share devcontainer ports race for the same local. The right model for most users is *connect == lifetime of forwards* — exit and the workstation goes back to clean.

This plan flips the default to session-scoped and makes the conflict path interactive. It does not remove the persistent-forward escape hatch; users who want today's behavior can pass `--keep-forwards` (or use `drift ports up` outside connect).

## What changes

### 1. `drift ports probe` folds in devcontainer.json

Server (`internal/server/kart_probe_ports.go`):
- Call `kart.ProbeForwardPorts(name)` alongside the `ss` invocation.
- Return both: extend `wire.KartProbePortsResult` with `DevcontainerPorts []int`. (Separate field rather than synthetic `ProbeListener` rows so the wire shape stays explicit and the client can label them differently.)
- Soften the `ss` error path: if the kart isn't running (or `devpod ssh` fails) return the devcontainer ports anyway with `Listeners=nil`. Probe should be useful pre-start. A wholly empty result still surfaces "no candidates"; failures only escalate when *both* sources are empty AND `ss` errored hard.

Client (`internal/cli/drift/ports_probe.go`):
- Build the candidate list from the union of `Listeners` and `DevcontainerPorts`. Dedupe on port number; if a port appears in both, prefer the listener entry (it has a process name).
- Picker rows tag the source: `:3000  vite` for live listeners, `:3000  (devcontainer)` for the spec. Sort by port.
- Existing exclusions (port 22, already-configured) still apply.

### 2. Per-port conflict prompt on `drift connect`

Hook (`internal/cli/drift/ports_connect.go`):
- Before calling `UnionDevcontainer`, walk the resolved devcontainer ports and partition into "free" and "conflicting" using the live prober + `state.PortsTaken()`.
- For each conflict on a TTY, prompt with the proposed remap target: `port 3000 in use on workstation. forward to :3001 instead? [Y/n]`. The proposed local is whatever `PickFreePort(prober, requested+1, taken)` would return — exposed via a small helper so the prompt and the eventual `AddForward` agree on the same value.
- `Y` (default, bare enter) → call `AddForward` with `requestedLocal = proposed`. Records `RemappedFrom=requested` for traceability.
- `n` → skip; the port is not added to state for this session. (The next connect will re-prompt; nothing persists.)
- Non-TTY (`!stdoutIsTTY` or `--output json`) keeps today's silent remap so scripts and CI are unaffected.
- Free ports go through the existing path unchanged.

This is a per-port prompt rather than a single batched one because users were explicit they want to *see* each proposed local before accepting. Conflicts are rare in practice (we tear down at exit, see §3), so 1-2 prompts is the realistic worst case.

### 3. Teardown on `drift connect` exit

`internal/connect/connect.go`:
- Add `AfterExec func(context.Context)` to `Deps`. Fire it after `Exec` returns regardless of exit code, with a fresh `context.Background()` (the parent ctx may be cancelled by ctrl-c).

`internal/cli/drift/connect.go` + a sibling helper to `ports_connect.go`:
- Bind `AfterExec` to a function that, for the connected kart:
  - Calls `driver.CancelForward` for every entry in `state.Get(circuit, kart)`.
  - Calls `driver.StopMaster` for the kart.
  - Leaves `ports.yaml` entries in place — they're the source of truth, next connect rebinds them.
- Best-effort: failures warn on stderr but don't change the user's exit code. Connect is the boss.

`drift connect` flag:
- `--keep-forwards` opt-out for users who want today's persistent behavior. When set, the AfterExec hook is a no-op.

### 4. Config switch to disable auto-forwarding entirely

`internal/config/client.go`:
- New `AutoForwardPorts *bool` (yaml `auto_forward_ports`) on `config.Client`. Tri-state via `*bool` so unset = default; `AutoForwardsPorts()` returns true when nil (mirroring `ManagesSSHConfig`).
- When false, both the `BeforeExec` reconcile and the `AfterExec` teardown become no-ops — drift connect leaves ports alone end-to-end. Users who run `drift ports add` / `up` / `down` manually still get them; the switch only affects connect-driven automation.

`internal/cli/drift/connect.go`:
- `doConnect` reads the loaded `Client` config (already loaded earlier in the connect path) and passes the effective `disable = NoForwards || !cfg.AutoForwardsPorts()` to both hooks. The `--no-forwards` flag keeps working as a per-invocation override.

Precedence:
1. `--no-forwards` flag (per-invocation, beats everything).
2. `auto_forward_ports: false` in config (persistent off).
3. Default on.

### 4. Plan 13 footer

A short note on plan 13 pointing at plan 15 for the updated lifecycle; the rest of plan 13 (state schema, reconcile, drivers) is unchanged.

## Test plan

- Unit: probe candidate union (listeners + devcontainer, dedup, label). Conflict prompt branching (Y / n / non-TTY). AfterExec wiring in `connect.Run` (table test: hook fires on success, on non-zero exit, on Exec error).
- Integration (`make integration`): run `drift connect` against a real devcontainer kart with `forwardPorts`, exit, assert `ssh -O check` reports the master gone and the local port is free.
- Manual: probe pre-start (devcontainer ports show up before the kart is up); conflict prompt with port pre-bound by `nc -l`; two karts sharing port 3000 sequentially.

## Out of scope

- Re-prompt UX in JSON mode. Scripts get silent remap; that's a feature.
- Cleaning state.yaml on exit (we explicitly *don't* — the file is the user's intent).
- Mid-session re-reconcile if state.yaml changes during a connect (today's behavior preserved).
