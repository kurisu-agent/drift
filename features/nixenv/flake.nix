{
  description = "drift devtools (nixenv variant) — nix-env's shellRc + zellij + statusbins, plus claude-code + drift-update";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    claude-code-nix = {
      url = "github:sadjow/claude-code-nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    # Defers to kurisu-agent/nix-env for the cross-cutting environment
    # configuration (zellij topbar, claude-status, OMP theme, eza theme,
    # rendered zshrc + bashrc-bootstrap, wrapped zsh + zellij). This flake
    # only adds the kart-specific layer: claude-code itself, drift-update,
    # and a bashrc prelude that translates ~/.drift/info.json (kartInfo
    # seed output) into the identity.json that nix-env's topbar reads.
    nix-env = {
      url = "github:kurisu-agent/nix-env";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, claude-code-nix, nix-env }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f system);

      # liveTools — drift-update can refresh these and the statusline
      # polls upstream for the version hint. Add an entry here, both the
      # CLI dispatch and the statusline gain support without further
      # wiring.
      liveTools = {
        claude = {
          flakeRef = "github:sadjow/claude-code-nix";
          attr     = "claude-code";
          versionProbe = {
            url     = "https://raw.githubusercontent.com/sadjow/claude-code-nix/main/package.nix";
            extract = ''version[[:space:]]*=[[:space:]]*"([^"]+)"'';
          };
        };
      };
    in
    {
      packages = forAllSystems (system:
        let
          pkgs       = nixpkgs.legacyPackages.${system};
          ne         = nix-env.lib.${system};
          ompTheme   = nix-env.packages.${system}.nix-env-omp-theme;
          claudeCode = claude-code-nix.packages.${system}.claude-code;

          # Bashrc prelude that runs *before* nix-env's standard bootstrap.
          # Reads ~/.drift/info.json (written by lakitu's kartInfo seed)
          # and renders the canonical $HOME/.config/zellij/identity.json
          # that nix-env's topbar reads. Also exports TZ — info.json
          # carries the host's /etc/timezone so child procs (zellij,
          # zjstatus, claude) all see the operator's wall clock.
          #
          # info.json is nested ({kart:{name,icon,color}, character:{name,
          # display_name,icon,color}, circuit:{name}, timezone}). nix-env's
          # identity.json is flat — translate at write time.
          #
          # identity.json shape:
          #   color : palette name from kart.color (kart-tinted topbar)
          #   name  : "<kart.name>.<circuit.name>" (kart segment)
          #   icon  : kart.icon when set, else random emoji (cached via
          #           the identity_file existence check, so each kart
          #           picks one once and reuses it across re-attaches)
          #   user  : character.display_name // character.name (friendly
          #           name on the topbar's right side; YAML key as fallback)
          driftBootstrapPrelude = ''
            if [ -r "$HOME/.drift/info.json" ] && command -v jq >/dev/null 2>&1; then
              tz=$(jq -r '.timezone // empty' "$HOME/.drift/info.json" 2>/dev/null || true)
              [ -n "$tz" ] && export TZ="$tz"

              identity_file="$HOME/.config/zellij/identity.json"
              if [ ! -f "$identity_file" ]; then
                mkdir -p "$(dirname "$identity_file")"

                icon=$(jq -r '.kart.icon // ""' "$HOME/.drift/info.json" 2>/dev/null || true)
                if [ -z "$icon" ]; then
                  # Random emoji default — deterministic per shell start
                  # and cached via the identity_file existence check, so
                  # each kart picks one once and reuses it across re-attaches.
                  emojis=(🦊 🐢 🦝 🦔 🐰 🦦 🐳 🦉 🐙 🐝 🦄 🐯 🦁 🐻 🐼 🐨 🦅 🦋 🐝 🦀 🦑 🌸 🍑 🌶 🍕 🍩 🥨 🌮 🍣 🥐)
                  icon="''${emojis[$RANDOM % ''${#emojis[@]}]}"
                fi

                jq --arg icon "$icon" '{
                  color: (.kart.color // ""),
                  name:  ((.kart.name // "?") + (if (.circuit.name // "") != "" then "." + .circuit.name else "" end)),
                  icon:  $icon,
                  user:  (.character.display_name // .character.name // "")
                }' "$HOME/.drift/info.json" > "$identity_file" 2>/dev/null || true
              fi
            fi
          '';

          # Drift-flavoured shellRc: nix-env's standard bootstrap + zshrc,
          # plus the info.json translation prelude and a yolo alias.
          shellRc = ne.zsh.mkShellRc {
            ompThemeJson       = ompTheme;
            extraBashrcPrelude = driftBootstrapPrelude;
            extraZshrc = ''
              # Drift-team alias.
              alias yolo='claude --dangerously-skip-permissions'
            '';
          };

          wrappedZellij = ne.zellij.mkWrappedBin {
            configDir = "${shellRc}/share/nix-env/zellij";
          };
          wrappedZsh   = ne.zsh.mkWrappedZsh { inherit shellRc; };
          claudeStatus = ne.claude.mkStatusBin {
            # Read the running claude version at runtime so drift-update's
            # priority-1 overlay shows up correctly (drift-update can
            # install a newer claude than the flake-pinned baseline).
            installedVersion = ''$(claude --version 2>/dev/null | awk '{print $1}' || printf unknown)'';
            pathPrefix       = "/workspaces/*";
            inherit (liveTools.claude) versionProbe;
          };

          # drift-update — overlays newer versions of registered live
          # tools into the user's nix profile at priority 1 (so they
          # shadow the flake-pinned baseline). `self [<flake-uri>]`
          # bumps the drift-devtools profile entry itself.
          driftUpdate = pkgs.writeShellApplication {
            name = "drift-update";
            runtimeInputs = [ pkgs.nix ];
            text = ''
              set -euo pipefail

              update_claude() {
                echo "==> Refreshing claude-code from ${liveTools.claude.flakeRef}#${liveTools.claude.attr}..."
                nix profile install \
                  --refresh \
                  --priority 1 \
                  '${liveTools.claude.flakeRef}#${liveTools.claude.attr}'
              }

              # The drift-devtools profile entry's *name* is determined by
              # nix at install time from the flake URL — typically it ends
              # up as "features/nixenv" (from the ?dir= segment), not
              # "drift-devtools" (the flake attribute). So matching by
              # name is fragile. `nix profile upgrade --all` sidesteps the
              # naming entirely; in a single-flake kart that's exactly
              # the right scope. With an explicit URI, remove every entry
              # whose locked URL points at our flake (regex on URL works
              # even when the name doesn't), then re-install fresh.
              update_self() {
                local new_uri="''${1:-}"
                if [ -n "$new_uri" ]; then
                  echo "==> Replacing existing drift-devtools entry with $new_uri..."
                  nix profile remove '.*drift-devtools.*' 2>/dev/null \
                    || nix profile remove '.*features/nixenv.*' 2>/dev/null \
                    || true
                  nix profile install --refresh "$new_uri"
                else
                  echo "==> Refreshing every nix-profile entry (--refresh --all)..."
                  nix profile upgrade --refresh --all
                fi
                echo
                echo "==> Restart zellij to pick up new layouts / shell config:"
                echo "    zellij kill-session main && exit"
                echo "    # then reconnect with 'drift connect <circuit> <kart>'"
              }

              known="claude self"

              case "''${1:-}" in
                --list)
                  echo claude
                  echo self
                  exit 0
                  ;;
                self)
                  shift
                  update_self "''${1:-}"
                  exit 0
                  ;;
              esac

              if [ "$#" -eq 0 ]; then
                set -- claude
              fi

              for tool in "$@"; do
                case "$tool" in
                  claude) update_claude ;;
                  self)   update_self ;;
                  *)
                    echo "drift-update: unknown tool '$tool' (known: $known)" >&2
                    echo "drift-update: 'drift-update --list' shows registered tools" >&2
                    exit 1
                    ;;
                esac
              done
            '';
          };

          drift-devtools = pkgs.symlinkJoin {
            name = "drift-devtools";
            paths = [
              # nix-env-shipped: shellRc tree, wrapped zellij + zsh,
              # zellij-status, claude-status.
              shellRc
              wrappedZellij
              wrappedZsh
              ne.zellij.statusBin
              claudeStatus

              # Drift-specific.
              claudeCode
              driftUpdate
            ] ++ (with pkgs; [
              # apt-set parity with devtools:2.
              fzf
              git
              curl
              unzip
              tmux
              iproute2
              procps
              jq

              # zsh plugins the rendered zshrc tries to source.
              zsh-autosuggestions
              zsh-syntax-highlighting

              # modern shell tooling.
              eza
              oh-my-posh
              yazi
              glow
              gh
              btop
            ]);
          };
        in
        {
          inherit drift-devtools shellRc;
          claude-status = claudeStatus;
          drift-update  = driftUpdate;
          claude-code   = claudeCode;
          default       = drift-devtools;
        });

      apps = forAllSystems (system: {
        drift-update = {
          type    = "app";
          program = "${self.packages.${system}.drift-update}/bin/drift-update";
        };
        claude-status = {
          type    = "app";
          program = "${self.packages.${system}.claude-status}/bin/nix-env-claude-status";
        };
        default = self.apps.${system}.drift-update;
      });
    };
}
