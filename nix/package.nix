# The command packages in cmd/. Also usable without flakes: see ../default.nix.
{
  lib,
  buildGoModule,
  version ? "0-unstable",
}:

buildGoModule {
  pname = "torrent";
  inherit version;

  src = lib.cleanSource ../.;

  # Regenerate after changing dependencies: set this to lib.fakeHash, run
  # `nix build .#torrent`, and copy the hash Nix reports back here.
  vendorHash = "sha256-agBFYl+WsI4h+Mp6Oz/cu2O2Yf2GeJnbh0T7hGzkyZM=";

  # go.work adds the possum storage backend from the storage/possum/lib git
  # submodule, which isn't part of this source tree and needs a Rust build.
  # None of the command packages use it. GOWORK is belt and braces for nixpkgs
  # revisions that don't apply postPatch to the vendor derivation.
  postPatch = ''
    rm -f go.work go.work.sum
  '';
  env.GOWORK = "off";

  subPackages = [
    "cmd/magnet-metainfo"
    "cmd/torrent"
    "cmd/torrent-pick"
    "cmd/torrent2"
  ];

  # cgo is on by default, and required for the libutp uTP implementation. See
  # utp_libutp.go: without it the pure Go fallback is used instead.

  ldflags = [
    "-s"
    "-w"
  ];

  # The test suite wants the network, and mounts FUSE filesystems. Run it from
  # the dev shell instead.
  doCheck = false;

  meta = {
    description = "BitTorrent client package, library and command line utilities";
    homepage = "https://github.com/anacrolix/torrent";
    changelog = "https://github.com/anacrolix/torrent/blob/master/CHANGELOG.md";
    license = lib.licenses.mpl20;
    mainProgram = "torrent";
    platforms = lib.platforms.unix;
  };
}
