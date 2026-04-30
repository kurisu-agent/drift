# Plan 17 — Circuit-local Nix binary cache

## Why

The Nix devcontainer feature (`ghcr.io/devcontainers/features/nix:1`) gives drift a uniform toolchain-delivery story: any glibc devcontainer image can have an arbitrary flake's closure installed at build time, regardless of language stack. The production `nixenv` tune does this against `github:kurisu-agent/drift?dir=features/nixenv#drift-devtools`.

The problem this plan addresses is the **cache-miss path**. Devpod builds devcontainer images by hashing `(base image + features merge)` into a content tag. When the hash matches a previous build, the entire image (including the 497 MB Nix-feature install layer) is reused bit-for-bit — that's already great. When the hash differs (different base image, different feature ordering, any change to the flake URI or `extraNixConfig`), the install layer is invalidated and Nix re-fetches every store path from `cache.nixos.org`. On a circuit with several karts on different stacks, this re-pays ~500 MB of WAN traffic per kart for the same content-addressed paths.

A circuit-local Nix substituter (e.g. `harmonia`, a Rust pull-through cache for `/nix/store` and upstream `cache.nixos.org`) collapses that cost to LAN bandwidth on first miss and zero on subsequent karts. Because Nix store paths are content-addressed, the same `nixpkgs#stdenv` derivation is reusable across any base image — the substituter wins exactly where the docker layer cache loses.

## Design choice — auto-inject, no tune-level flag

An earlier sketch routed this through a `nix_cache: true` field on the tune that lakitu would expand into the right `extraNixConfig` snippet at kart-create time. The simpler shape is no new tune field at all: when lakitu detects (a) the circuit cache is enabled, (b) the tune's resolved features include the Nix feature, and (c) the tune has not already set its own `substituters = …` line in `extraNixConfig`, it appends the circuit cache's `substituters` and `trusted-public-keys` automatically. Tunes that want a different substituter (cachix, an upstream private cache, etc.) simply set their own `substituters` line and the auto-injector backs off.

This means a circuit operator's only step is `services.lakitu.nixCache.enable = true` + rebuild; every nix-using tune on that circuit gets cache-accelerated builds with no further action. Tune authors keep an escape hatch (set your own `substituters`) and a manual paste path for unusual cases (`lakitu nix-cache info`, see §2).

Tradeoff: there is now a small piece of "magic" in the tune→kart resolution path. We mitigate by (a) showing the post-injection features in `lakitu kart show <name>`, so the actual config a kart was built with is always visible, and (b) gating the injection on a clear, single-rule detection (presence of the Nix feature + absence of a `substituters` line).

## What changes

### 1. NixOS module — `services.lakitu.nixCache`

`nix/modules/lakitu.nix` gains a sub-block:

```nix
options.services.lakitu.nixCache = {
  enable = mkEnableOption "circuit-local Nix binary cache (harmonia pull-through)";
  port   = mkOption { type = types.port; default = 5000; };
  bind   = mkOption {
    type = types.str;
    default = "0.0.0.0";
    description = "Listen address. Default exposes the cache to anything that can reach the circuit's network — including kart containers via the docker bridge. Override to a specific interface (e.g. the docker0 host IP) to narrow the blast radius.";
  };
  signKeyPath = mkOption {
    type = types.path;
    description = "Path to a Nix signing private key. If the file does not exist at activation, a fresh keypair is generated; the public key is written next to it as `<signKeyPath>.pub`.";
  };
  upstream = mkOption {
    type = types.listOf types.str;
    default = [ "https://cache.nixos.org" ];
    description = "Upstream substituters this cache pulls through.";
  };
};
```

Implementation:
- Wires `services.harmonia` from nixpkgs with the chosen bind/port/signKeyPath.
- Activation script generates the keypair on first boot if `signKeyPath` doesn't exist (`nix-store --generate-binary-cache-key <hostname>-1 priv pub`), refusing to overwrite if it does.
- Opens the firewall on the chosen port for the docker bridge interface (`networking.firewall.interfaces.docker0.allowedTCPPorts`). Does not touch the public-interface firewall — the operator explicitly opens it if they want LAN-wide access.
- No automatic enable: `services.lakitu.enable = true` does not turn on the cache. Operators opt in with `services.lakitu.nixCache.enable = true`. Default-off because the cache is stateful (signing key, populated store) in a way the rest of the lakitu module is not.

### 2. CLI — discovery surface

`lakitu nix-cache info` (new subcommand under existing `lakitu` Kong tree):
- Reads `services.lakitu.nixCache` from a small marker file dropped by the NixOS module (`/run/lakitu/nix-cache.json` written by `system.activationScripts`), containing `{ url, pubkey, upstream }`.
- Prints both human-readable and `--output json` forms. Human form is paste-ready:
  ```
  Substituter: http://circuit-host:5000
  Public key:  circuit-host-1:abc123...
  Upstreams:   https://cache.nixos.org
  
  To use in a tune, add to `extraNixConfig`:
  
    substituters = http://circuit-host:5000 https://cache.nixos.org
    trusted-public-keys = circuit-host-1:abc123... cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=
  ```
- Exits non-zero with a clear message if the marker file is missing (cache not enabled / not yet activated).
- No corresponding `drift nix-cache info` — this is a circuit-side concern, exposed over RPC only if cross-circuit users start asking for it. Out of scope for v1.

The "what hostname goes in the URL" question is unsolvable from inside the NixOS module alone (the module can't know which hostname the user's karts will reach it as — `localhost` is wrong from inside docker, the docker bridge IP is right but only from kart containers, the LAN hostname is right for cross-host circuits). Resolution:
- Default to `services.lakitu.nixCache.advertisedUrl` if set, else compute `http://<docker0-host-ip>:<port>` (works for the dominant single-host case where karts run on the same machine as lakitu).
- Document the override clearly. Wrong default here is annoying but recoverable — users get a bad first paste, fix the option, paste again.

### 3. Auto-injection in the tune→kart features resolver

Where: wherever the existing per-kart features JSON is computed by merging `tune.features` with `--features` overrides. (Likely `internal/kart/flags.go` or a sibling — phase 1 will pin the exact site.)

Algorithm, applied after the existing merge produces the final features map:

```
if !nixCache.Enabled() { return features }
for key, opts := range features {
    if !strings.HasPrefix(key, "ghcr.io/devcontainers/features/nix") { continue }
    extra, _ := opts["extraNixConfig"].(string)
    if substitutersLineRE.MatchString(extra) { continue }   // user opted out by setting their own
    opts["extraNixConfig"] = appendSubstituters(extra, nixCache)
    features[key] = opts
}
return features
```

`substitutersLineRE` is anchored: `(?m)^\s*substituters\s*=`. We deliberately do *not* try to merge our cache into a user-supplied substituters list — if they wrote one, we trust their list verbatim. They can paste our cache in via `lakitu nix-cache info` if they want both.

`appendSubstituters` writes two newline-separated lines after whatever the user already had:

```
substituters = http://<advertisedUrl> https://cache.nixos.org
trusted-public-keys = <pubkey> cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY=
```

`cache.nixos.org` is preserved in the substituters list so misses on the local cache transparently fall through. The pubkey is whatever the cache's marker file currently advertises — re-read on each kart-create, so a `signKeyPath` regeneration takes effect on the next kart without bouncing lakitu.

Visibility: `lakitu kart show <name>` (and `info`) already returns the resolved feature JSON; the injection is naturally visible there. `lakitu tune show <name>` continues to show only what the tune *declares* — the injection happens at resolution, not at tune storage. This split is intentional: a tune is portable across circuits, and serializing the cache details into the tune would baking circuit-specific URLs into a circuit-independent object.

Failure modes:
- Marker file missing while `cfg.Enabled()` returns true (race against an unfinished rebuild): treat as cache-not-yet-ready, fall through to no injection, no error. The next kart picks it up once activation completes.
- Marker file present but stale (cache disabled but rebuild hadn't fired): caller can purge it manually; we don't auto-clean. Activation script rewrites/removes on each rebuild.

### 4. Manual escape hatch — `extraNixConfig` paste

Two cases the auto-injector deliberately doesn't cover:
- A tune wants the circuit cache *and* an additional upstream (cachix, private cache).
- A tune is being authored on a workstation and tested against multiple circuits, where hard-coding any single URL is wrong.

Both are served by `lakitu nix-cache info` (§2): paste the printed block into the tune's `extraNixConfig`, set `substituters` explicitly, and the auto-injector backs off.

### 5. Tune-side documentation

A short section in the README (no new doc tree) walking through:
1. Enable the cache: `services.lakitu.nixCache.enable = true;` in the consumer's NixOS config; rebuild.
2. (Optional) verify: `lakitu nix-cache info` shows what will be injected.
3. Just use any nix-using tune. `drift new <kart> --tune <name>` — first kart populates the cache, second kart on a different base image hits it. No per-tune change required.
4. (Optional override) if you need a specific substituter list, set `extraNixConfig: "substituters = …"` on the tune yourself; the auto-injector backs off.

No tune YAML schema change. No drift-side change. No lakitu RPC change for v1.

### 4. Verification

- Unit-equivalent: a small integration test under `integration/` that stands up two karts with different base images and the same `extraNixConfig` cache block, asserts the second kart's `kart new` doesn't re-fetch from `cache.nixos.org` (proxy log inspection on the harmonia side, or `nix store ping` from inside the kart against the substituter URL). Gated behind the `integration` build tag like the rest.
- Manual repro: create two karts that share the same Nix-feature flake but pick different devcontainer base images (so the docker layer cache misses) and time both builds. With the cache off, the second build re-fetches the same closure from `cache.nixos.org`; with the cache on, the second build hits the circuit.

## What is explicitly out of scope

- **Multi-circuit / federated caching.** Each circuit has its own substituter; if karts on circuit A want to hit circuit B's cache, the tune's `substituters` line lists both. No cross-circuit coordination.
- **Garbage collection policy.** harmonia delegates to the host's `/nix/store`, so circuit-side `nix-collect-garbage` is the lever. Out of scope to wrap.
- **Auth.** harmonia supports it; we don't enable it. The cache contains nixpkgs-derived store paths with no secrets in them; the signing key proves provenance, not access. If a circuit operator wants auth they can put harmonia behind a reverse proxy.
- **Replacing `cache.nixos.org` as the upstream.** `services.lakitu.nixCache.upstream` is a list, but defaults to `cache.nixos.org` only. Adding `cachix.org/<org>` or similar is a per-deployment knob, not something drift opinions on.
- **`drift` workstation-side awareness.** Workstations don't run karts; the cache benefit is entirely circuit-local. No `drift` command changes.

## Phased rollout

1. Module + harmonia wiring + activation key generation + marker file. Verify on a circuit by enabling the option and confirming `curl http://localhost:5000/nix-cache-info` returns a valid response.
2. `lakitu nix-cache info` subcommand reading the marker file.
3. Auto-injection in the tune→kart features resolver, with unit tests for: (a) cache enabled + nix feature + no user substituters → injection happens; (b) cache enabled + nix feature + user substituters present → no injection; (c) cache disabled → no injection; (d) tune has no nix feature → no injection.
4. README paragraph.
5. Integration test that confirms the second kart on a different base image hits the local cache (proxy log inspection, or `nix store ping` from inside the kart).
6. Rip the corresponding TODO entry out of `TODO.md`.

Each phase is mergeable independently. Phase 1 alone gives operators a working cache they can paste from. Phase 3 is the "magic" step — easiest to revert if it bites us, and the manual paste path keeps working either way.
