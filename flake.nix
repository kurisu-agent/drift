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
        version = "v0.17.0";
        # hash of the upstream source tarball (set both together when bumping).
        srcHash    = "sha256-quWYRn2vJEeHaUQC1GFnOqQuRPsjmWbDuLFTFX0frS0=";
        vendorHash = "sha256-TztljwhE4w4D7IYkkA/9E61JZEb+999XSGkZv/RUOsg=";
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
          # Drift's go.mod pins Go 1.25 via the `toolchain` directive. Prefer
          # the matching nixpkgs attribute; fall back to `pkgs.go` only when
          # go_1_25 is missing from the channel bump.
          goToolchain =
            if pkgs ? go_1_25 then pkgs.go_1_25 else pkgs.go;
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
          driftLdflags = [
            "-s" "-w"
            "-X github.com/kurisu-agent/drift/internal/version.Version=${self.shortRev or "dev"}"
            "-X github.com/kurisu-agent/drift/internal/devpod.ExpectedVersion=${devpodPin.version}"
          ];

          mkDriftBinary = name: goBuild {
            pname   = name;
            version = self.shortRev or "dev";
            src     = pkgs.lib.cleanSource ./.;
            # Recompute with `nix build .#lakitu 2>&1` and paste the hash in
            # when bumping dependencies.
            vendorHash = "sha256-/VO5hRXWylEkqtAtnnlTN0ySo881+4GjZfnEEbDzT0Q=";
            subPackages = [ "cmd/${name}" ];
            env.CGO_ENABLED = 0;
            ldflags = driftLdflags;
            doCheck = false;
            meta.mainProgram = name;
          };
        in
        {
          packages = {
            inherit devpod;
            drift   = mkDriftBinary "drift";
            lakitu  = mkDriftBinary "lakitu";
            default = mkDriftBinary "drift";
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
        });

  # ---------------------------------------------------------------------------
  # Manual install (buildGoModule output is now a first-class flake package —
  # `nix build .#drift`, `.#lakitu`, `.#devpod` — but the tarball flow from
  # GoReleaser remains the documented production path until the NixOS module
  # lands. See plans/PLAN.md § Future and § Bootstrap / install.
  # ---------------------------------------------------------------------------
}
