{
  description = "drift — client (drift) + server (lakitu) binaries for remote devcontainer workspaces.";

  # To regenerate flake.lock after editing inputs, run `nix flake update`
  # from a machine with Nix enabled. Do not commit a stale lock after
  # bumping nixpkgs.
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    let
      # -----------------------------------------------------------------------
      # Pins. Every consumer of `drift` should build against these exact
      # artifacts. The devpod pin flows into lakitu via ldflags so the binary
      # knows which version it expects at runtime (see internal/devpod.Verify
      # and `lakitu init`'s version-check output).
      # -----------------------------------------------------------------------
      devpodPin = {
        owner   = "skevetter";
        repo    = "devpod";
        version = "v0.22.0";
        # When bumping `version`, both hashes need refreshing. The idiom:
        # set each to "sha256-AAAA...=" (44 As), run `nix build .#devpod`,
        # and paste the `got:` value from the first failure into srcHash;
        # rerun and paste the second `got:` into vendorHash.
        srcHash    = "sha256-MWl+c/IdrizoUMwlMegvJXJ8oerbVw3OPzxHuzMvZSc=";
        vendorHash = "sha256-hCFvOVqtjvbP+pCbAS1LOcFHLFJLkki7DnZmQDr6QFQ=";
      };
    in
    flake-utils.lib.eachSystem
      [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ]
      (system:
        let
          pkgs = import nixpkgs { inherit system; };
          # Drift's go.mod pins Go 1.26 via the `toolchain` directive. Prefer
          # the matching nixpkgs attribute; fall back to `pkgs.go` only when
          # go_1_26 is missing from the channel bump.
          goToolchain =
            if pkgs ? go_1_26 then pkgs.go_1_26 else pkgs.go;
          goBuild = pkgs.buildGoModule.override { go = goToolchain; };

          # devpod — built from the pinned fork so every drift release ships
          # with the exact binary it was tested against.
          devpod = goBuild {
            pname = "devpod";
            inherit (devpodPin) version;
            src = pkgs.fetchFromGitHub {
              inherit (devpodPin) owner repo;
              rev  = devpodPin.version;
              hash = devpodPin.srcHash;
            };
            vendorHash = devpodPin.vendorHash;
            env.CGO_ENABLED = 0;
            ldflags = [ "-X github.com/skevetter/devpod/pkg/version.version=${devpodPin.version}" ];
            excludedPackages = [ "./e2e" ];
            doCheck = false;
            meta.mainProgram = "devpod";
          };

          # Shared ldflags for drift + lakitu: version info + the devpod pin
          # so `lakitu init` can compare the circuit's devpod against what
          # this binary was built against.
          #
          # Version stays "dev" for nix-built binaries — there's no native
          # way to pluck the latest git tag from a flake attribute, and
          # release builds come through goreleaser (.goreleaser.yaml) which
          # injects Version={{.Version}} + Commit={{.Commit}} itself.
          # Commit is wired so `drift --version` renders a short hash
          # suffix even on untagged dev builds.
          driftLdflags = [
            "-s" "-w"
            "-X github.com/kurisu-agent/drift/internal/version.Version=dev"
            "-X github.com/kurisu-agent/drift/internal/version.Commit=${self.rev or "dirty"}"
            "-X github.com/kurisu-agent/drift/internal/devpod.ExpectedVersion=${devpodPin.version}"
          ];

          mkDriftBinary = name: goBuild {
            pname   = name;
            version = self.shortRev or "dev";
            src     = pkgs.lib.cleanSource ./.;
            # Recompute with `nix build .#lakitu 2>&1` and paste the hash in
            # when bumping dependencies.
            vendorHash = "sha256-xeClHP26CzyQ0pVN6mhMha7+DcEpUD/GlarsODn2vNc=";
            subPackages = [ "cmd/${name}" ];
            env.CGO_ENABLED = 0;
            ldflags = driftLdflags;
            doCheck = false;
            meta.mainProgram = name;
          };
        in
        {
          packages = rec {
            inherit devpod;
            drift   = mkDriftBinary "drift";
            lakitu  = mkDriftBinary "lakitu";
            default = drift;

            # Circuit-side runtime: one install target for provisioning a
            # remote devcontainer host. `nix profile install
            # github:kurisu-agent/drift#circuit` drops lakitu + pinned
            # devpod + mosh into the user's profile.
            circuit = pkgs.symlinkJoin {
              name = "drift-circuit";
              paths = [ lakitu devpod pkgs.mosh ];
            };
          };

          devShells.default = pkgs.mkShell {
            name = "drift-dev";
            packages = [
              goToolchain
              pkgs.golangci-lint
              pkgs.goreleaser
              pkgs.govulncheck
              pkgs.gnumake
              pkgs.git
              devpod
            ];

            shellHook = ''
              echo "drift dev shell — go $(go version | awk '{print $3}'), devpod ${devpodPin.version}"
            '';
          };

          # formatter makes `nix fmt` work; handy for CI later.
          formatter = pkgs.nixpkgs-fmt;

          # `nix flake check` runs this: build a minimal NixOS eval that
          # imports the lakitu module and assert:
          #   - lakitu/devpod end up on environment.systemPackages,
          #   - the lakitu-kart@ user-unit template is registered, and
          #   - DEVPOD_HOME is NOT set as a session variable — scoping
          #     it globally leaks into every user's plain `devpod`
          #     invocations (https://github.com/kurisu-agent/drift/pull/9
          #     landed the first version of this module with that env
          #     var, and we ripped it out once `kart.connect` replaced
          #     the need for it). The negative assertion guards against
          #     a well-meaning future edit putting it back.
          checks.lakitu-module = let
            eval = nixpkgs.lib.nixosSystem {
              inherit system;
              modules = [
                self.nixosModules.lakitu
                ({ ... }: {
                  # Minimum required to satisfy nixosSystem evaluation.
                  # We only care about the options the module writes to;
                  # these are stubs for everything else.
                  boot.loader.grub.enable    = false;
                  fileSystems."/"            = { device = "tmpfs"; fsType = "tmpfs"; };
                  system.stateVersion        = "24.11";
                  nixpkgs.hostPlatform       = system;
                })
              ];
            };
            cfg    = eval.config;
            sysEnv = cfg.environment.sessionVariables;
            kart   = cfg.systemd.user.services."lakitu-kart@";
            hasPkg = name: builtins.any
              (p: (p.pname or p.name or "") == name || pkgs.lib.hasInfix name (p.name or ""))
              cfg.environment.systemPackages;
          in
            assert !(sysEnv ? DEVPOD_HOME);
            assert kart.description == "drift kart %i (autostart via lakitu)";
            assert hasPkg "lakitu";
            assert hasPkg "devpod";
            pkgs.runCommand "drift-lakitu-module-check" { } ''
              touch $out
            '';
        })
      //
      # nixosModules sit outside `eachSystem`: modules are evaluated by
      # the consumer's system (during their nixos-rebuild), not by this
      # flake's eval, so they're architecture-agnostic and must live at
      # the top level. `default` points at lakitu so a bare
      # `imports = [ drift.nixosModules.default ];` works too.
      {
        nixosModules.lakitu  = import ./nix/modules/lakitu.nix { inherit self; };
        nixosModules.default = self.nixosModules.lakitu;
      };
}
