# Entry point for Nix without flakes: nix-build. Flake users get the same
# package from `nix build .#torrent`.
{ pkgs ? import <nixpkgs> { } }:

pkgs.callPackage ./nix/package.nix { }
