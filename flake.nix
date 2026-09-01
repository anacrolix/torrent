{
  description = "BitTorrent client package, library and command line utilities";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      # There are no version tags in the source tree, so date the build from the
      # last commit touching it.
      date = self.lastModifiedDate or "19700101000000";
      ymd = n: builtins.substring n 2 date;
      version = "0-unstable-${builtins.substring 0 4 date}-${ymd 4}-${ymd 6}";
    in
    {
      packages = forAllSystems (pkgs: rec {
        torrent = pkgs.callPackage ./nix/package.nix { inherit version; };
        default = torrent;
      });

      # Everything needed to run `just test` from a checkout: the Go toolchain,
      # Rust for the possum submodule, and FUSE for the torrentfs tests.
      devShells = forAllSystems (pkgs: {
        default = pkgs.callPackage ./nix/shell.nix { };
      });

      checks = forAllSystems (pkgs: {
        torrent = self.packages.${pkgs.stdenv.hostPlatform.system}.torrent;
      });

      formatter = forAllSystems (pkgs: pkgs.nixfmt-rfc-style);
    };
}
