{
  description = "Anki statistics exporter";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/319e4cebef10cbb636c6ea392137b52ad105336a";

  outputs = { self, nixpkgs }:
    let
      systems = [ "aarch64-darwin" "aarch64-linux" "x86_64-linux" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in {
      devShells = forAllSystems (system: let pkgs = nixpkgs.legacyPackages.${system}; in {
        default = pkgs.mkShell {
          packages = [ pkgs.go pkgs.gopls (pkgs.lib.getBin pkgs.postgresql) ];
          CGO_ENABLED = "1";
          shellHook = ''
            export GOPATH="$PWD/.cache/go"
            export GOCACHE="$PWD/.cache/go-build"
          '';
        };
      });
      packages = forAllSystems (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          build = subPackages: pkgs.buildGoModule {
            name = "anker";
            src = pkgs.lib.fileset.toSource {
              root = ./.;
              fileset = pkgs.lib.fileset.unions [ ./cmd ./internal ./scripts ./go.mod ./go.sum ];
            };
            vendorHash = "sha256-yaT6tFDjTU0Z5+gAdUx6wlJlRQRhc/N0SREvVARVzx0=";
            inherit subPackages;
            env.CGO_ENABLED = "1";
            nativeBuildInputs = [ (pkgs.lib.getBin pkgs.postgresql) ];
            checkPhase = ''
              runHook preCheck
              bash scripts/test-postgres.sh
              go vet ./...
              runHook postCheck
            '';
          };
        in {
          default = build [ "cmd/anker" ];
          # The default package already runs tests for every Go package.
          demo-server = (build [ "cmd/anker-demo" ]).overrideAttrs { doCheck = false; };
        });
      apps = forAllSystems (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          localApp = mode:
            let runner = pkgs.writeShellApplication {
              name = "anker-${mode}";
              runtimeInputs = [ (pkgs.lib.getBin pkgs.postgresql) pkgs.curl pkgs.jq ];
              text = ''
                ${pkgs.lib.optionalString (mode == "dev") ''
                  env_file="''${ANKER_ENV_FILE:-$PWD/.env}"
                  if [ -f "$env_file" ]; then
                    set -a
                    # shellcheck disable=SC1090
                    source "$env_file"
                    set +a
                  fi
                ''}
                exec bash ${self}/scripts/run-local.sh ${mode} \
                  ${self.packages.${system}.default}/bin/anker \
                  ${self.packages.${system}.demo-server}/bin/anker-demo "$@"
              '';
            };
            in {
              type = "app";
              program = "${runner}/bin/anker-${mode}";
              meta.description = if mode == "demo"
                then "Run synthetic Anki statistics with disposable PostgreSQL"
                else "Run the configured Anki account with disposable PostgreSQL";
            };
        in { demo = localApp "demo"; dev = localApp "dev"; });
      checks = forAllSystems (system: { build = self.packages.${system}.default; });
    };
}
