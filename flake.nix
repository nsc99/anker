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
      packages = forAllSystems (system: let pkgs = nixpkgs.legacyPackages.${system}; in {
        default = pkgs.buildGoModule {
          name = "anker";
          src = pkgs.lib.fileset.toSource {
            root = ./.;
            fileset = pkgs.lib.fileset.unions [ ./cmd ./internal ./scripts ./go.mod ./go.sum ];
          };
          vendorHash = "sha256-yaT6tFDjTU0Z5+gAdUx6wlJlRQRhc/N0SREvVARVzx0=";
          subPackages = [ "cmd/anker" ];
          env.CGO_ENABLED = "1";
          nativeBuildInputs = [ (pkgs.lib.getBin pkgs.postgresql) ];
          checkPhase = ''
            runHook preCheck
            bash scripts/test-postgres.sh
            go vet ./...
            runHook postCheck
          '';
        };
      });
      checks = forAllSystems (system: { build = self.packages.${system}.default; });
    };
}
