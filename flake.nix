{
  description = "drift — dev shell for building drift + lakitu";

  # To regenerate flake.lock after editing inputs, run `nix flake update`
  # from a machine with Nix enabled. Do not commit a stale lock after
  # bumping nixpkgs.
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
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
          # Prefer the pinned Go 1.25 toolchain. If the attribute is not
          # available on a future nixpkgs bump, fall back to `pkgs.go` — the
          # go.mod toolchain directive will still pin the exact patch version
          # for reproducibility.
          goToolchain =
            if pkgs ? go_1_25 then pkgs.go_1_25 else pkgs.go;
        in
        {
          devShells.default = pkgs.mkShell {
            name = "drift-dev";
            packages = [
              goToolchain
              pkgs.golangci-lint
              pkgs.goreleaser
              pkgs.govulncheck
              pkgs.gnumake
              pkgs.git
            ];

            shellHook = ''
              echo "drift dev shell — go $(go version | awk '{print $3}')"
            '';
          };

          # formatter makes `nix fmt` work; handy for CI later.
          formatter = pkgs.nixpkgs-fmt;
        });

  # ---------------------------------------------------------------------------
  # Manual install (no Nix packaging yet — buildGoModule output is Future scope,
  # per plans/PLAN.md § Future and § Bootstrap / install).
  #
  #   1. Download the release tarball for your OS/arch from the GitHub
  #      Releases page (produced by `.goreleaser.yaml`):
  #        - drift:  drift_<version>_<os>_<arch>.tar.gz
  #        - lakitu: lakitu_<version>_linux_<arch>.tar.gz
  #   2. Verify checksums against `checksums.txt`.
  #   3. Extract and copy the binaries into /usr/local/bin (or any PATH dir):
  #        sudo install -m 0755 drift  /usr/local/bin/drift
  #        sudo install -m 0755 lakitu /usr/local/bin/lakitu
  #   4. On each circuit, run `lakitu init` to bootstrap ~/.drift/garage.
  #      Follow the printed checklist for linger, systemd template,
  #      mosh-server, and docker group membership (the items a future NixOS
  #      module will automate).
  #   5. On each workstation, run `drift warmup` to register circuits +
  #      characters.
  # ---------------------------------------------------------------------------
}
