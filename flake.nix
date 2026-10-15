{
  description = "Anki statistics exporter";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/319e4cebef10cbb636c6ea392137b52ad105336a";

  outputs = { self, nixpkgs }:
    let
      systems = [ "aarch64-darwin" "x86_64-darwin" "aarch64-linux" "x86_64-linux" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in {
      devShells = forAllSystems (system: let pkgs = nixpkgs.legacyPackages.${system}; in {
        default = pkgs.mkShell {
          packages = [ pkgs.go pkgs.gopls ];
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
            fileset = pkgs.lib.fileset.unions [ ./cmd ./internal ./go.mod ./go.sum ];
          };
          vendorHash = "sha256-A/4+dZgywEjH5C/2G+vu6jB3t+xXzraFcp3qEulJtFE=";
          subPackages = [ "cmd/anker" ];
          env.CGO_ENABLED = "1";
          checkPhase = ''
            runHook preCheck
            go test ./...
            go vet ./...
            runHook postCheck
          '';
        };
      });
      checks = forAllSystems (system: { build = self.packages.${system}.default; });
    };
}
