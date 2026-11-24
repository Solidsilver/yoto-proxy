{
  description = "yoto-proxy: serve Navidrome playlists and albums to Yoto Make Your Own cards";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "aarch64-darwin"
      ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in
    {
      packages = forAllSystems (
        system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
        in
        rec {
          yoto-proxy = pkgs.callPackage ./nix/package.nix { };
          default = yoto-proxy;
        }
      );

      devShells = forAllSystems (system: {
        default = nixpkgs.legacyPackages.${system}.mkShell {
          packages = with nixpkgs.legacyPackages.${system}; [
            go
            nodejs
            pnpm
            sqlite
          ];
        };
      });

      nixosModules.default = import ./nix/module.nix self;
    };
}
