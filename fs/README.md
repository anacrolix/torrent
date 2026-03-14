# torrent/fs

Package `torrentfs` exposes a torrent client as a read-only filesystem.

It is FUSE-library-agnostic: the core types (`TorrentFS`, `Backend`,
`Unmounter`) and helpers live here; concrete FUSE backends are in separate
modules:

| Repo | FUSE library |
|------|-------------|
| [anacrolix/og-torrentfs](https://github.com/anacrolix/og-torrentfs) | [anacrolix/fuse](https://github.com/anacrolix/fuse) — supports macFUSE and fuse-t on macOS, fusermount on Linux |
| [anacrolix/hanwen-torrentfs](https://github.com/anacrolix/hanwen-torrentfs) | [hanwen/go-fuse/v2](https://github.com/hanwen/go-fuse) — uses macFUSE socket protocol on macOS (incompatible with fuse-t) |

## Changing this package

- **Interface changes** (`Backend`, `Unmounter`, or any exported type/function
  used by backends): update both backend repos and verify their CI passes.
- **Helper changes** (`traverse.go`, `fileread.go`): check both backends still
  compile and their tests pass.
- **Test suite changes** (`tfstest/`): both backends import and run
  `tfstest.RunTestSuite`; update them if the `MountFunc` signature or
  `RunTestSuite` API changes.
- **go.mod / dependency changes**: backends pin a specific commit of this
  module via a pseudo-version (no `replace` directives). After merging, update
  the `require` line in each backend's `go.mod` to the new commit hash.
