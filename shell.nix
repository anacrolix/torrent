# Entry point for Nix without flakes: nix-shell. Flake users get the same
# environment from `nix develop`.
{ pkgs ? import <nixpkgs> { } }:

pkgs.callPackage ./nix/shell.nix { }
