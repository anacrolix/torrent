# Development environment. Also usable without flakes: see ../shell.nix.
{
  lib,
  stdenv,
  mkShell,
  cargo,
  # Not packaged for darwin, where macFUSE is used instead.
  fuse ? null,
  git,
  go,
  golangci-lint,
  gopls,
  just,
  rustc,
}:

mkShell {
  packages = [
    go
    gopls
    golangci-lint
    just
    git
    # storage/possum/lib is a Rust library built by `just build-possum`.
    cargo
    rustc
  ]
  # Needed by the torrentfs tests. Note that mounting also needs a setuid
  # fusermount, which on NixOS means enabling programs.fuse.userAllowOther or
  # equivalent.
  ++ lib.optionals (stdenv.hostPlatform.isLinux && fuse != null) [ fuse ];

  shellHook = ''
    # go.work can require a newer toolchain than the nixpkgs Go. Let Go fetch
    # it rather than fail, unless the caller has an opinion.
    export GOTOOLCHAIN="''${GOTOOLCHAIN:-auto}"

    if [ -e go.work ] && [ ! -e storage/possum/lib/Cargo.toml ]; then
      echo "torrent: the possum submodule isn't checked out; run" >&2
      echo "  git submodule update --init --recursive" >&2
      echo "or set GOWORK=off to build without it." >&2
    fi
  '';
}
