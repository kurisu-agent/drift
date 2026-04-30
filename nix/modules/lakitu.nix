# lakitu — NixOS module.
#
# A drift "circuit" is any host that runs `lakitu` as its RPC server so
# a workstation with `drift` can create / start / connect to karts on it.
# Importing this module provisions everything `drift` expects the host to
# have:
#
#   - `lakitu`, `devpod`, and `mosh` on the system PATH (so sshd-spawned
#     sessions and systemd units can exec them without PATH surgery).
#   - A user-level `lakitu-kart@<name>.service` systemd template so
#     `drift enable <name>` has something to hand off to on each login.
#     Linger is deliberately NOT enabled here — which user(s) should have
#     their session bus survive logout is a sysadmin decision, not lakitu's.
#     Enable it per-user in your host config: `users.users.<n>.linger = true;`
#
# # Usage
#
# ```nix
# # flake.nix (consumer)
# inputs.drift.url = "github:kurisu-agent/drift";
# # NixOS system
# imports = [ drift.nixosModules.lakitu ];
# ```
#
# That one import is the whole contract. Override package pins (e.g. to
# plug in a live-working-tree build on a dev VM) via the `services.lakitu`
# options below — everything else has safe defaults pinned to this flake.
{ self }:
{ config, lib, pkgs, ... }:

let
  cfg = config.services.lakitu;
  # Resolve "this system" once so the default packages match the host's
  # architecture without every option duplicating the expression.
  driftPkgs = self.packages.${pkgs.stdenv.hostPlatform.system};
in
{
  options.services.lakitu = {
    enable = lib.mkOption {
      type        = lib.types.bool;
      default     = true;
      description = ''
        Whether this host should be set up as a drift circuit. Defaults to
        true because the only reason to import this module is to opt in;
        the flag exists so a consumer can conditionally disable it without
        ripping the import back out.
      '';
    };

    package = lib.mkOption {
      type        = lib.types.package;
      default     = driftPkgs.lakitu;
      defaultText = lib.literalExpression "drift.packages.\${system}.lakitu";
      description = ''
        The lakitu binary to install. Override to plug in a live-working-
        tree build (`buildGoModule` against a local checkout) on the dev
        VM without pulling in anything else from the flake.
      '';
    };

    devpodPackage = lib.mkOption {
      type        = lib.types.package;
      default     = driftPkgs.devpod;
      defaultText = lib.literalExpression "drift.packages.\${system}.devpod";
      description = ''
        The devpod binary lakitu delegates to. Default is the version this
        drift flake pins (lakitu checks for an exact match at init time).
      '';
    };

    moshPackage = lib.mkOption {
      type        = lib.types.nullOr lib.types.package;
      default     = pkgs.mosh;
      defaultText = lib.literalExpression "pkgs.mosh";
      description = ''
        The mosh binary used for interactive `drift connect` sessions.
        Set to `null` on hosts that can't or won't install mosh — drift
        falls back to plain ssh automatically.
      '';
    };

    nixCache = {
      # Circuit-local Nix binary cache. Opt-in even when `services.lakitu.enable`
      # is true, because the cache is stateful (signing key, populated /nix/store)
      # in a way the rest of the lakitu module is not. See plans/17-circuit-nix-cache.md.
      enable = lib.mkEnableOption "circuit-local Nix binary cache (harmonia)";

      port = lib.mkOption {
        type        = lib.types.port;
        default     = 5000;
        description = "TCP port harmonia listens on.";
      };

      bind = lib.mkOption {
        type        = lib.types.str;
        default     = "0.0.0.0";
        description = ''
          Address harmonia binds to. Default exposes the cache on every
          interface so docker-bridge containers (kart workspaces) and any
          LAN host can reach it. Override to a specific interface address
          (e.g. the docker0 host IP) to narrow the blast radius.
        '';
      };

      signKeyPath = lib.mkOption {
        type        = lib.types.path;
        default     = "/var/lib/lakitu/nix-cache-key";
        description = ''
          Path to the Nix signing private key. The matching public key is
          stored alongside as `<signKeyPath>.pub`. If neither file exists at
          activation time, a fresh keypair is generated automatically; if a
          private key exists the activation script leaves it alone.
        '';
      };

      upstream = lib.mkOption {
        type        = lib.types.listOf lib.types.str;
        default     = [ "https://cache.nixos.org" ];
        description = ''
          Upstream substituters advertised alongside this cache. These
          appear in the marker file (and in the snippet `lakitu nix-cache
          info` prints) so karts fall through to upstream on local-cache
          miss without an extra paste step.
        '';
      };

      advertisedUrl = lib.mkOption {
        type        = lib.types.str;
        default     = "http://172.17.0.1:${toString config.services.lakitu.nixCache.port}";
        defaultText = lib.literalExpression ''"http://172.17.0.1:''${toString config.services.lakitu.nixCache.port}"'';
        description = ''
          URL kart containers should use to reach the cache. The default
          assumes docker on a single-host circuit (172.17.0.1 is the
          standard docker0 host IP). Override to the circuit's LAN hostname
          for multi-host setups, or to a specific interface address for
          narrower exposure.
        '';
      };

      workers = lib.mkOption {
        type        = lib.types.int;
        default     = 8;
        description = ''
          Number of harmonia worker threads. Default upstream is 4 which
          underuses common circuit hosts; bumping to 8 roughly doubles
          aggregate throughput for multi-path installs (kart.new on a
          fresh tune typically pulls 30+ paths in parallel). Diminishing
          returns past CPU-count, no benefit for single-large-path
          fetches — those are bound by nginxCache below.
        '';
      };

      nginxCache = {
        # Reverse-proxy disk cache in front of harmonia, opt-out via
        # `nginxCache.enable = false`. Harmonia's NAR generation is
        # single-threaded per HTTP request and CPU-bound at ~18 MB/s on
        # typical hardware — a single 200 MB nixpkgs source path takes
        # 10-12 s every fetch because nars are computed on the fly with
        # no built-in caching. nginx caches the upstream NAR response
        # to disk on first hit; subsequent fetches serve from page
        # cache at near-IO speed (>1 GB/s) and the harmonia worker is
        # free for new paths.
        enable = lib.mkOption {
          type        = lib.types.bool;
          default     = true;
          description = ''
            Whether to wrap harmonia in an nginx proxy_cache layer.
            Default on because the speedup on repeat fetches is large
            (10× or more for big paths) and the cost is modest (one
            extra service + ~10 GB of disk by default). Turn off to
            expose harmonia directly on `nixCache.bind:port`.
          '';
        };

        proxyPort = lib.mkOption {
          type        = lib.types.port;
          default     = 5001;
          description = ''
            Internal port harmonia binds to when nginxCache is enabled.
            Only nginx (loopback) talks to it; firewall stays closed.
            Move if you already run something on 5001.
          '';
        };

        maxSize = lib.mkOption {
          type        = lib.types.str;
          default     = "10g";
          description = ''
            nginx proxy_cache disk size cap (LRU evicted past it). 10G
            fits a few projects' worth of nixpkgs closures comfortably;
            bump for circuits hosting more karts. Cache dir is under
            `/var/cache/nginx/harmonia/`.
          '';
        };

        inactive = lib.mkOption {
          type        = lib.types.str;
          default     = "30d";
          description = ''
            Time after which un-fetched cache entries are evicted even
            if `maxSize` isn't hit. Long default because store paths
            are content-addressed — a path that hasn't been fetched in
            30 days is unlikely to be needed soon, and re-fetching
            falls through to harmonia at its native speed anyway.
          '';
        };
      };
    };
  };

  config = lib.mkIf cfg.enable (lib.mkMerge [{
    environment.systemPackages =
      [ cfg.package cfg.devpodPackage ]
      ++ lib.optional (cfg.moshPackage != null) cfg.moshPackage;

    # NOTE: this module deliberately does NOT set DEVPOD_HOME in pam_env
    # or /etc/set-environment. An earlier iteration did, to make `drift
    # connect`'s `ssh host devpod ssh <kart>` see the drift-managed devpod
    # state, but that forced a global override onto every invocation of
    # `devpod` — including plain user-facing ones — which broke their
    # personal devpod workspaces. Since lakitu v0.6, `kart.connect` returns
    # the fully-resolved remote argv (`env DEVPOD_HOME=... /abs/devpod
    # ssh <kart> --set-env ...`) so the DEVPOD_HOME scope lives on the
    # single remote command line and nothing else is affected. See
    # internal/server/kart_connect.go for the server handler and
    # internal/connect/connect.go for the client's fetchConnectArgv.

    # User-level template: `systemctl --user start lakitu-kart@<name>` is
    # what `drift enable <name>` wires up. Runs once per boot per kart (it
    # shells `lakitu start <kart>`, which is itself idempotent), so Type
    # stays oneshot with RemainAfterExit so `is-active` reports true after
    # the initial start.
    systemd.user.services."lakitu-kart@" = {
      description = "drift kart %i (autostart via lakitu)";
      after       = [ "network-online.target" ];
      wants       = [ "network-online.target" ];
      serviceConfig = {
        Type            = "oneshot";
        ExecStart       = "${lib.getExe cfg.package} start %i";
        RemainAfterExit = true;
        Restart         = "no";
        TimeoutStartSec = 300;
      };
    };
  }
  # Circuit-local Nix binary cache (plan 17). Stands up harmonia as a
  # pull-from-/nix/store substituter so karts on different base images can
  # reuse content-addressed store paths off-LAN instead of refetching from
  # cache.nixos.org every time the docker layer cache misses. Opt-in
  # because it owns persistent state (signing key, /nix/store growth);
  # `services.lakitu.enable` alone does not turn it on.
  (lib.mkIf cfg.nixCache.enable (let
    nc = cfg.nixCache;
    # When the nginx cache layer is on, harmonia hides on loopback and
    # nginx takes the public bind. When it's off, harmonia goes on the
    # public bind directly.
    harmoniaBind =
      if nc.nginxCache.enable
      then "127.0.0.1:${toString nc.nginxCache.proxyPort}"
      else "${nc.bind}:${toString nc.port}";
  in {
    services.harmonia = {
      enable       = true;
      signKeyPaths = [ nc.signKeyPath ];
      settings = {
        bind    = harmoniaBind;
        workers = nc.workers;
      };
    };

    # Open the firewall on the docker bridge so kart containers can reach
    # the cache (whether they hit nginx or harmonia directly) without
    # exposing it on the public interface. Operators who want LAN-wide
    # access (multi-host circuits) open the relevant interface in their
    # host config; we deliberately don't presume.
    networking.firewall.interfaces."docker0".allowedTCPPorts = [ nc.port ];

    # Generate a signing keypair on first activation if none exists. The
    # private key lives at signKeyPath; the matching public key is written
    # alongside as <signKeyPath>.pub so the marker-file step below can
    # read it back without re-deriving from the private key.
    system.activationScripts.lakituNixCacheKey = lib.stringAfter [ "users" "groups" ] ''
      keyDir=$(${pkgs.coreutils}/bin/dirname ${lib.escapeShellArg cfg.nixCache.signKeyPath})
      if [ ! -f ${lib.escapeShellArg cfg.nixCache.signKeyPath} ]; then
        ${pkgs.coreutils}/bin/mkdir -p "$keyDir"
        ${pkgs.nix}/bin/nix-store --generate-binary-cache-key \
          ${lib.escapeShellArg "${config.networking.hostName}-1"} \
          ${lib.escapeShellArg cfg.nixCache.signKeyPath} \
          ${lib.escapeShellArg "${cfg.nixCache.signKeyPath}.pub"}
        ${pkgs.coreutils}/bin/chmod 0600 ${lib.escapeShellArg cfg.nixCache.signKeyPath}
        ${pkgs.coreutils}/bin/chmod 0644 ${lib.escapeShellArg "${cfg.nixCache.signKeyPath}.pub"}
      fi
      # harmonia runs as the `harmonia` system user (created by services.harmonia)
      # and needs read access to the private key. Re-chown each activation so a
      # signKeyPath rotation or recovery from a botched chown self-heals.
      if id harmonia >/dev/null 2>&1 && [ -f ${lib.escapeShellArg cfg.nixCache.signKeyPath} ]; then
        ${pkgs.coreutils}/bin/chown harmonia:harmonia ${lib.escapeShellArg cfg.nixCache.signKeyPath} ${lib.escapeShellArg "${cfg.nixCache.signKeyPath}.pub"} 2>/dev/null || true
      fi
    '';

    # Marker file at /run/lakitu/nix-cache.json. Tmpfs is intentional —
    # it gets rewritten on every activation, so a `services.lakitu.nixCache.*`
    # change followed by `nixos-rebuild switch` is reflected immediately
    # without lakitu having to reload anything. Phase 2 (`lakitu nix-cache
    # info`) and phase 3 (auto-injection) read this file at runtime.
    system.activationScripts.lakituNixCacheMarker = lib.stringAfter [ "lakituNixCacheKey" ] ''
      ${pkgs.coreutils}/bin/mkdir -p /run/lakitu
      pubkey=""
      if [ -f ${lib.escapeShellArg "${cfg.nixCache.signKeyPath}.pub"} ]; then
        pubkey=$(${pkgs.coreutils}/bin/cat ${lib.escapeShellArg "${cfg.nixCache.signKeyPath}.pub"})
      fi
      ${pkgs.coreutils}/bin/cat > /run/lakitu/nix-cache.json <<MARKER_EOF
      {
        "url": ${builtins.toJSON cfg.nixCache.advertisedUrl},
        "pubkey": "$pubkey",
        "upstream": ${builtins.toJSON cfg.nixCache.upstream}
      }
      MARKER_EOF
      ${pkgs.coreutils}/bin/chmod 0644 /run/lakitu/nix-cache.json
    '';

    # Reverse-proxy disk cache. nginx serves on the public bind/port,
    # caches GETs to /var/cache/nginx/harmonia/, falls through to
    # harmonia on loopback for cache misses. proxy_cache_lock collapses
    # concurrent misses on the same key into a single upstream fetch.
    # Opt-out via services.lakitu.nixCache.nginxCache.enable = false.
    services.nginx = lib.mkIf nc.nginxCache.enable {
      enable = true;
      proxyCachePath."harmonia" = {
        enable       = true;
        levels       = "1:2";
        keysZoneName = "harmonia";
        keysZoneSize = "100m";
        maxSize      = nc.nginxCache.maxSize;
        inactive     = nc.nginxCache.inactive;
      };
      virtualHosts."lakitu-nix-cache" = {
        listen = [{ addr = nc.bind; port = nc.port; }];
        locations."/" = {
          proxyPass = "http://127.0.0.1:${toString nc.nginxCache.proxyPort}";
          extraConfig = ''
            proxy_cache harmonia;
            proxy_cache_lock on;
            proxy_cache_lock_timeout 30s;
            proxy_cache_use_stale updating;
            proxy_cache_valid 200 ${nc.nginxCache.inactive};
            proxy_cache_valid 404 1m;
            proxy_cache_valid any 30s;
            proxy_cache_revalidate on;
            proxy_buffering on;
            proxy_read_timeout 600s;
            proxy_send_timeout 600s;
            client_max_body_size 0;
            add_header X-Cache-Status $upstream_cache_status always;
          '';
        };
      };
    };
  }))
  ]);
}
