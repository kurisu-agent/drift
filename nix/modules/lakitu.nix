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
  };

  config = lib.mkIf cfg.enable {
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
  };
}
