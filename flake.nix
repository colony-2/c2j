{
  description = "The standalone c2j executable";

  # 26.05 still supports Intel macOS, unlike newer unstable snapshots.
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-26.05";

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" "x86_64-darwin" "aarch64-darwin" ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in {
      packages = forAllSystems (system:
        let
          c2j = nixpkgs.legacyPackages.${system}.callPackage ./nix/package.nix {
            version = "unstable-${self.shortRev or "dirty"}";
            revision = self.rev or "dirty";
          };
        in { inherit c2j; default = c2j; });
      checks = forAllSystems (system: {
        c2j = self.packages.${system}.c2j;
      });
    };
}
