# Changelog

All notable changes to [anacrolix/torrent](https://github.com/anacrolix/torrent) are documented in this file.

## [Unreleased]

- Add retry limit to `listenAll` to prevent infinite loops
- Retry any subsequent listen failure on dynamic port
- Error trying to open `mmapFileIo` after close
- Check `Torrent.haveInfo` when writing announce record status progress
- Use a heap in `Client.startPieceHashers`
- Batch torrent input updates by doing them in the dispatcher
- Close shared readers when storage is closed
- Use `InsteadOf` and various other indexed improvements to manage announce overdue

## [v1.61.0] - 2025-12-17

Large release focused on **client-level announce dispatching** and **indexed btree performance**.

- Refactor toward client and torrent-wide announcing with dispatcher architecture (`b7f1ef92`, `64861fe3`, `7c3533f6`)
- Implement indexed btree set using tidwall, then switch back to ajwerner (`169f5c4f`, `d2b53161`)
- Fix panic calling `Torrent.AddTrackers` on a dropped torrent
- Add weakrefs to `Torrent` for announces after a torrent is dropped (`0e90e666`)
- Implement `Completed` and `Stopped` announce events in dispatcher
- Fix really inefficient memory allocation in smartban (`4f0e00d2`)
- Add idle timeout reader to webseeds (`83e7a66c`)
- Fix panic on webseeds without trailing `/` for directory torrents
- Avoid new connections if download rate is flooded (`05398af0`)
- Fix rate limiter usage
- CI fixes for Erigon 3.3 releases
- Close file handles on rename for part file promotion
- Add CI test for global installability
- Unswitch `cl.forwardPort` (#1024)
- Add debug message: rejecting metadata piece (#1023)
- Slow down DHT announcing on errors

## [v1.60.0] - 2025-09-03

Large release with **webseed improvements**, **mmap file IO**, and **part file support**.

- Add mmap alternative IO system for file storage (`df616a51`)
- Start implementing part file support for file storage (`f71b6374`)
- Implement `io.WriterTo` for file storage pieces for hashing performance
- Skip holes with `mmapFileIo` and in file piece `WriteTo`
- Shorten webseed requests by default (`6a1e8a4e`)
- Fix webseed unique request key changing at end of slice
- Compare Client and per-Torrent active webseed maps and reuse large allocations
- Fix msync on unaligned pages on unix
- Make `storage.PieceCompletePersistenter` for backwards compatibility
- Time storage flushing based on piece completion persistence
- Remove the upload goroutine spam

## [v1.59.1] - 2025-08-18

- Remove possum storage replace

## [v1.59.0] - 2025-08-15

Large release with extensive **webseed overhaul**, **mmap storage**, and **performance optimizations**.

- Set default file IO implementation to mmap (`8fe0dba0`)
- Set default webseed host request concurrency to 25
- Block `Torrent.Complete` on hashing
- Implement multiple chunk reads for webseed (`a4efd622`)
- Keep webseed requests below client limit and update synchronously
- Add `MetainfoSourcesMerger` and separate `MetainfoSourcesClient`
- Add `ClientConfig.Slogger` and improve slog integration throughout
- Add `TORRENT_MAX_ACTIVE_PIECE_HASHERS` and `TORRENT_WEBSEED_REQUEST_CHUNK_SIZE` env vars
- Truncate webseed requests to response body cache boundaries
- Drop torrents on `Client.Close` not just close them
- Add global webseed requests on a timer (`87b3196e`)
- Move `request-strategy` into internal
- Refactor all use of `frankban/quicktest` to `go-quicktest/qt`
- Switch to `github.com/anacrolix/fuse@v0.4.0`
- Add `Reader.SetContext` to deprecated `ReadContext`
- Expose `TORRENT_WEBSEED_HOST_REQUEST_CONCURRENCY` env var
- Limit piece hashers per client
- Set pieces incomplete when files go missing or are truncated
- Add file handle caching (`68cb87fd`)
- Derive initial completion from part files by default
- Add `IgnoreUnverifiedPieceCompletion` and move fields to `AddTorrentOpts`
- Add `DialForPeerConns` config option
- Expose `PieceRequestOrder.Iter` and `PieceRequestOrderItem` for debugging
- Add `Torrent.VerifyDataContext` and `Piece.VerifyDataContext`
- Support setting the Type-Of-Service field to 'throughput' for sockets (#1017)
- Expose torrent Peer status updates (#987)
- Updates to actions/cache v3 (#1000)
- Convert `GetRequestablePieces` to an iterator
- Use `unique.Handle` for webseed URL keys and piece order infohashes
- Fix various webseed request, cancellation, and rate limiting issues
- Many logging improvements

## [v1.58.1] - 2025-02-16

- Fix building go-libutp with C17
- WebSeed seems supported (#993)

## [v1.58.0] - 2024-10-18

- Upgrade pion/webrtc to v4 (#985)
- Expose WebRTC peerconn stats (#983)
- Only apply WebRTC data channel write limit to webtorrent peer conns
- Use stdlib replacements of `golang.org/x/exp` and newer multiless
- Refactor storage tests to `go-quicktest/qt`
- Set `ConnectionIdMismatchNul` error default log level to debug

## [v1.57.1] - 2024-09-28

- Rework handshake parsing
- Remove use of missinggo perf

## [v1.57.0] - 2024-09-09

- Basic support for responding to `HashRequest` (BEP 52)
- Support encoding `Hashes` & `HashReject` messages
- Fix webseed stall on request errors (`ef889a26`, `b3aea1a6`)
- Fix webseeds not requesting after priorities are already set
- Basic support for serializing v2 torrent file (#968)
- Add context to MSE handshakes (`b7b97a66`, `2502dd29`)
- Be more pedantic about trailing data in `metainfo.Load` and `bencode.Unmarshal`
- Support unmarshalling into maps with non-string key types (bencode)
- Add `Context` param to `ClientImpl.OpenTorrent` for logging
- Add peer bootstrapping integration test
- Make `Torrent.Complete` a method returning a read-only interface
- Fix sqlite piece completion error when dirs are missing
- tracker/udp: Reset connection ID on error response

## [v1.56.1] - 2024-06-11

- Correctly hash hybrid torrents with trailing v1 padding file

## [v1.56.0] - 2024-06-03

Large release with **BitTorrent v2 support** and many community contributions.

- Implement BitTorrent v2 torrent handling (`eaaa9c0b`, `0e781be1`, `fef859aa`)
- v2 piece hashing, merkle hashing, and v2 file handling
- Send v2 upgrade bit and track v2 state in peer conn
- Read piece hashes from peers (hash request/reject/hashes)
- Announce to both v1 and v2 swarms
- Handle v2 Torrents added by short infohash only
- Implement reading piece layers from peers
- Add `MagnetV2` and `infohash_v2`
- Add BEP 47 extended file attributes fields
- Export `PiecePriority` and fix downloading occurring without piece priority
- Add `Torrent.ModifyTrackers()` func (#945)
- Fix UPnP clear loop trap (#946) and clear port mappings on client close (#942)
- Optimize memory usage by avoiding intermediate buffer in message serialization (#928)
- Support ICE servers auth (#920)
- `file.Flush()` (#937)
- Fix invalid UTF-8 name with `BestName` and `BestPath` (#915)
- Add possum storage support
- Clone func used since go 1.21 (#947)
- Add low level support for BEP 10 user protocols
- Support operating when IPv6 isn't available
- Various v2 hash fixes and improvements
- Add `mapstructure` tags in MetaInfo

## [v1.55.0] - 2024-02-28

- Add BitTorrent v2 fields, consts and reference torrents (`3ad279e8`)
- Add possum storage (`47267688`)
- Add low level support for BEP 10 user protocols (`8605abc7`)
- Support operating when IPv6 isn't available
- Cache sqlite storage capacity for a short while
- Remove mutex in MSE cipher reader
- Use Go 1.22 in CI

## [v1.54.1] - 2024-02-20

- Try to do smart ban block hashing without client lock
- Switch to xxHash for smartban cache, reduce allocations
- Add smart ban hash benchmarks

## [v1.54.0] - 2024-02-09

- Optimize request strategy for torrent storage with unlimited capacity
- Remove runtime correctness check in `GetRequestablePieces` (#902)
- Stay interested when the peer has pieces we don't have
- Save extra HTTP Request alloc in webseed request

## [v1.53.3] - 2024-01-16

- Fix race in `ExportStatusWriter`
- Keep client lock when iterating over connections in `Torrent.KnownSwarm` (#893)
- Retract versions affected by indefinite outgoing requests bug
- Fix requests still being made when downloading is disallowed
- Expect cancel acknowledgement from Transmission v4 clients
- `Torrent.WebseedPeerConns()` method (#883)

## [v1.53.2] - 2023-12-04

- `Torrent.WebseedPeerConns()` method (#883) (backport)

## [v1.53.1] - 2023-10-10

- Drop support for go 1.20
- Don't run fs tests on Windows

## [v1.53.0] - 2023-10-09

- Support scraping from HTTP trackers (`11833b45`)
- Merge fs module back into the root module
- Configurable hashers amount per torrent (#867)
- Fix request heap pop bug
- Switch to `github.com/go-llsqlite/adapter`
- Add `ClientConfig.WebTransport`
- fs: Use a new torrent file reader per handled request
- fs: Mount read-only, return file permissions for root node

## [v1.52.8-beta] - 2024-04-20

- Cherry-pick webseed update timer fix
- Body close simplify (#933)

## [v1.52.7] - 2023-12-04

- `Torrent.WebseedPeerConns()` method (#883) (backport)

## [v1.52.6] - 2023-08-23

- Fix request heap pop bug (backport)
- Fix race in webseed requester sleep duration calculation
- Note that `torrent.Reader` is not concurrent-safe
- Ditch `lispad/go-generics-tools` for `anacrolix/generics/heap`

## [v1.52.5] - 2023-08-14

- Remove unnecessary use of `SO_REUSEPORT`

## [v1.52.4] - 2023-07-23

- Fix torrent file real time completed bytes
- Fix UDP tracker panic
- Get go-libutp fix for go1.21
- Remove refs to deprecated `io/ioutil`

## [v1.52.3] - 2023-06-26

- Fix error unmarshalling bad metainfo nodes field

## [v1.52.2] - 2023-06-26

- Test and fix closed sqlite storage panicking during piece hashing

## [v1.52.1] - 2023-06-23

- Fix `UseSources` panicking when sqlite storage is closed
- Log bad tracker URL error
- Fix incorrect EOF when decoding some peer protocol message types

## [v1.52.0] - 2023-05-24

- Reintroduce torrent-wide PEX throttling
- Attribute accepted connection to holepunching when connect message is late
- Fix filecache issues on Windows
- Fix `addrPortOrZero` for unix sockets on Windows
- Various Windows test fixes
- Make fs a separate module
- Run Go CI test job on macOS and Windows
- Add WebRTC ICE servers config (#824)

## [v1.51.3] - 2023-05-24

- Fix `ClientConfig.Logger.SetHandlers` being clobbered

## [v1.51.2] - 2023-05-23

- Add holepunch metrics and message fuzzing
- Print peer ID in ASCII-only
- Fix panic logging unknown holepunch error code

## [v1.51.1] - 2023-05-19

- Report LTEP extensions in status output
- Move `PeerClientName` and `PeerExtensionIDs` to `PeerConn`
- Fix issue #795 (#807)
- Add doc comment for `Torrent.BytesMissing`

## [v1.51.0] - 2023-05-19

Large release with **holepunching support** (ut_holepunch, BEP 55).

- Implement `ut_holepunch` protocol support (`e86e6244`)
- Attempt holepunch after initial dial fails
- Add holepunching stats and tests
- Support multiple ongoing half-open attempts
- Add `Client.Stats` with `ActiveHalfOpenAttempts`
- Add `ClientConfig.DialRateLimiter`
- Rate limit received PEX messages per connection
- Fix overflow in average download rate
- Use `netip.AddrPort` in PEX code and filter unusable addrs much sooner
- Prefer outgoing connections from higher to lower peer IDs
- Try to balance incoming and outgoing conns per torrent
- Switch Go CI to go1.20

## [v1.50.0] - 2023-04-04

- Add `AddTorrentOpts.InfoBytes`
- Expose `StringAddr`
- Import generics as `g`

## [v1.49.1] - 2023-04-03

- bencode: Only use `unsafe.String` for go>=1.20

## [v1.49.0] - 2023-03-20

- Add `Peer.Torrent` accessor
- Expose UDP tracker error response type
- Start a UDP tracker server implementation (`eb9c032f`)
- Support HTTP tracker server
- Support upstream trackers and alternate remote host resolution
- Add upstream announce gating and tracing
- Limit peer request data allocation
- Forward leechers and seeders announce handler results
- Default to announcing as leecher
- bencode: Support parsing strings into bool

## [v1.48.1] - 2023-02-18

- Fix negative bencode string length parsing
- Check for chunks overflowing piece bounds on request read
- Sleep webseed peers after unhandled errors
- Update tidwall/btree

## [v1.48.0] - 2022-12-17

- Change default webseed path escaping to work for all S3-compatible providers
- Add fuzzing for webseed path escaping
- Support custom headers when dialling WS connection to tracker (#789)
- Modify HTTP request before sending (#787)
- Support providing a `DialContext` for the HTTP client (#786)
- Pass `TrackerDialContext` to webtorrent trackers (#785)
- Retrieve file via HTTP URL in metainfo (#778)
- Move many types into subpackages
- Group Client DHT and tracker config
- Support marshalling http tracker response peers
- Run default DHT with table maintainer

## [v1.47.0] - 2022-09-18

- Limit amount of peers per UDP packet (#771)
- Use `RLock` where possible (#766, #767)
- Add OpenTelemetry tracing to webtorrent WebRTC resources
- Support announcing to webtorrent trackers without offers
- `cmd/torrent create`: Add piece length and private options
- `cmd/torrent serve`: Support multiple file paths
- Add ability to set `DialContext`/`ListenPacket` for tracker announcements (#760)
- Do torrent storage flush on piece completion (#755)
- Use `metainfo.ChoosePieceLength` from more locations

## [v1.46.0] - 2022-06-25

- Check that incoming peer request chunk lengths don't exceed upload rate limiter burst size
- Optimize torrent piece length (#758)
- Add `Client.PublicIPs`
- Update tidwall/btree (0.7.2 -> 1.3.1) (#757)

## [v1.45.0] - 2022-06-20

- Rework peer connection writer to keep individual writes smaller
- `cmd/torrent serve`: Print magnet link with tracker defaults
- Bump up the local client reqq

## [v1.44.0] - 2022-06-01

- Revert "Switch requestState to be a slice"
- Implement a public `Peer.DownloadRate` (#750)

## [v1.43.1] - 2022-05-12

- Use `Option` for cached `Torrent` length
- Set debug log level for outgoing connection error

## [v1.43.0] - 2022-05-11

- Add fallback piece ordering for non-readahead priorities
- Use reusable roaring iterators
- Default 64 MiB max unverified bytes
- Use a generic heap implementation for request selection
- Order readahead requests by piece index
- Add typed roaring bitmap
- Infer `pp.Decoder.MaxLength` from chunk size (#743)
- `cmd/torrent`: Export Prometheus metrics
- Add custom URL encoder for webseeds (#740)
- Use Torrent/PeerConn logger instead of default logger (#736, #740)

## [v1.42.0] - 2022-04-11

- Implement smart banning using generics (`53cf5080`)
- Support banning webseeds and dropping matching peers on IP ban
- Switch to ajwerner/btree and tidwall/btree generics for piece request ordering
- Expose `Torrent.UseSources` and `Torrent.AddWebSeeds`
- Add `cmd/torrent-create -u` and `-i` flags
- Use HTTP proxy config for torrent sources
- Expose `webseed.EscapePath`
- Pull `GO_LOG` support from anacrolix/log
- Exclusively use crawshaw instead of zombiezen for sqlite
- Add scrape subcommand to `cmd/torrent`
- Fix v4 in v6 IPs from being banned as IPv4
- Fix race in `MergeSpec` using `DisableInitialPieceCheck`
- go1.18 support

## [v1.41.0] - 2022-02-20

- Format code with gofumpt (#724)
- Pass client logger to go-libutp sockets (#722)
- Pull webrtc SCTP Transport wasm support
- Use webrtc selected ICE candidate for peer addresses
- Switch from `missinggo/MultiLess` to `anacrolix/multiless`
- bencode: Support decoding `""` as dict key
- bencode: Return `ErrUnexpectedEOF` instead of EOF in the middle of values
- Add `bencode.Decoder.MaxStrLen`
- Reject peer requests on data read failures

## [v1.40.2] - 2022-02-11

- Backport: Reject peer requests on data read failures

## [v1.40.1] - 2021-12-27

- Reject peer requests on data read failures
- Reduce some logging

## [v1.40.0] - 2021-12-27

- Use relative availabilities to determine piece request order (`506ff8d0`)
- Dynamic outbound max requests
- Allow stealing from slower connections within priority classes
- Apply download rate limiter to webseeds
- Make `Torrent.cancelRequestsForPiece` more efficient

## [v1.39.2] - 2021-12-16

- Retract v1.39.0 due to peer-requesting issues
- Fix various benchmark failures
- Optimizations in `PieceRequestOrder.Update` and item comparisons
- Fix go-libutp import when CGO is disabled

## [v1.39.1] - 2021-12-13

- Add `Peer.cancelAllRequests` for webseedPeer
- Update requests after deleting all in some corner cases
- Update peer requests if not interested when piece priorities change

## [v1.39.0] - 2021-12-12

Large **request strategy overhaul** with piece request ordering, caching, and work stealing.

- Implement piece request ordering with retained state (`94bb5d40`)
- Cache piece request orderings
- Partition piece request strategy by storage capacity key
- Try request stealing between connections
- Use zombiezen sqlite for piece completion
- Various bencode fixes: integer parsing, dict key ordering, leading zeroes
- PEX: fluid event log and use new `NodeAddr` search methods
- Do webseed request updates asynchronously
- Handle 503 returns from webseed peer endpoints

## [v1.38.1] - 2021-12-12

- Retract last few minor versions with peer-requesting issues

## [v1.38.0] - 2021-11-16

- Switch to `github.com/anacrolix/fuse`
- Add `Reader.SetReadaheadFunc` with context
- Provide context to readahead func

## [v1.37.0] - 2021-11-12

- Improve error handling for bad webseeds
- Pass HTTP Proxy config into webseeding HTTP client
- Allow non-partial webseed part responses for small files
- Don't use non-directory webseed URLs for multi-file torrents
- Increment webseed peer piece availability

## [v1.36.0] - 2021-11-10

- `cmd/torrent`: Add `serve` subcommand
- `cmd/torrent`: Add `bencode {json,spew}` commands
- Export `addTorrentOpts`
- bencode: Fix marshalling of unaddressable array of bytes

## [v1.35.0] - 2021-11-01

- Add custom DNS lookup function for tracker scraper (#trackerscraper)
- Fix unnecessary modification of `Torrent.CancelPieces` API
- bencode: Encode arrays of bytes as strings

## [v1.34.1] - 2021-10-29

- Fix `Torrent.CancelPieces` API (backport)
- Check if torrent is closed before handling peer request data read failures
- Fix panic in benchmark

## [v1.34.0] - 2021-10-27

Large release implementing **per-peer requesting** and **BEP 6 fast extension** compliance.

- Change peer requesting to spread requests out evenly (`0f53cbf0`)
- Wait for cancelled requests to be rejected per the spec (BEP 6)
- Don't automatically delete requests if choked with fast extension
- Handle allowed fast while choked when requests exist in same piece
- Implement pending requests using BSI (bitmap index)
- Add `DisableInitialPieceCheck` option (#677)
- Support minimum peer extensions
- Track requests preserved across chokings
- Use roaring bitmap for pending pieces
- Add reasons for `updateRequests` to be triggered

## [v1.33.1] - 2021-10-28

- go mod download fix

## [v1.33.0] - 2021-10-07

- Return errors from `Client.Close`
- Upgrade `Torrent.GotInfo` with chansync
- Switch `Peer._peerPieces` to use raw roaring Bitmap type
- Apply some lints from GoLand

## [v1.32.0] - 2021-09-30

Large **performance optimization** release with many community contributions.

- Extensive bencode optimizations (#649, #651, #652, #656)
- Many inlineable method optimizations (#577, #594, #601, #603, #605, #612, #615, #621, #622, #623, #625, #626, #633)
- Drop `bradfitz/iter`, `xerrors`, and `missinggo/slices` dependencies
- Default sqrt readahead algorithm
- Begin extracting `squirrel` from storage/sqlite
- Track dirty chunks in a single bitmap on Torrent
- Use an iterator to skip through dirty chunks
- Rework Reader waiting and storage close handling
- Create default constructor for Client (#567)
- Fix http announce of infohash containing `' '` bytes (#591)

## [v1.31.0] - 2021-09-02

- Add `storage.NewFileOpts` with "no name" handling
- Fix panic on double Close of sqlite piece completion DB
- Fix some DeepSource lints

## [v1.30.4] - 2021-08-22

- Avoid reallocating keep alive timer on each pass
- Don't run linter on master branch

## [v1.30.3] - 2021-08-19

- Fix data race closing incoming PeerConn
- Fix deadlock when checking whether to send keep alive
- Rewrite `peerConnMsgWriter.run`
- Add linter CI (#542)
- Use `roaring.Bitmap` directly for completed pieces

## [v1.30.2] - 2021-08-13

- Fix mmap panic on darwin with Go 1.17

## [v1.30.1] - 2021-08-12

- Fix panic unmarshalling bencode dict into unsupported type
- Fix allocation of empty `DhtNodes` in `TorrentSpec`

## [v1.30.0] - 2021-08-11

- Full-client request strategy implementation (`56e5d08e`)
- Rework `storage.TorrentImpl` to support shared capacity key
- Add client-level max unverified bytes
- Apply next request state asynchronously
- Extract request strategy into a separate module
- Track piece availability at the Torrent-level
- Fix sqlite piece completion
- Determine peer max requests based on receive speed
- Create go.yml CI workflow (#497)
- Build tags to disable packages if necessary (#499)

## [v1.29.2] - 2021-07-26

- Trim UDP tracker client read allocations
- Close torrent storage asynchronously on drop

## [v1.29.1] - 2021-07-14

- Fix `go:build` directives

## [v1.29.0] - 2021-06-27

Large release: **rewritten UDP tracker client** and **storage hashing API**.

- Rewrite UDP tracker client (`333c878d`)
- Extract HTTP tracker client into separate package
- Extract protocol-agnostic tracker Client
- Allow storage backends to do their own hashing (#518)
- Add `ClientConfig.AcceptPeerConnections`
- Remove conntrack, expose `Torrent.AnnounceToDht`
- Add `storage/disabled`
- bencode: Improve support for embedded structs and anonymous pointers
- Add `DialFirst` exposure
- Add explicit metadata extension types
- Remove sqlite piece-resource storage

## [v1.28.0] - 2021-05-14

- Default to sqlite piece completion for dir if cgo enabled
- Implement sqlite directly without using piece resources (`afea2809`)
- Expose `SetSynchronous` and various blob cleanup styles
- Track download rate and chunks received in nested expvar
- Big rename of files and types in storage
- Create SECURITY.md

## [v1.27.0] - 2021-05-09

- Allow custom `PieceCompletion` (#486)
- Change `ClientImpl` to `ClientImplCloser`

## [v1.26.1] - 2021-05-04

- Big logging cleanup to improve experience from README

## [v1.26.0] - 2021-03-25

- Make tracker order in `Metainfo.Magnet` deterministic

## [v1.25.1] - 2021-03-12

- Upgrade to pion/webrtc@v3
- PEX: impede full-meshing in tracker-less swarms with cooldown
- Include webseed URLs in `Torrent.Metainfo` output
- Treat 404 responses from webseed peers as fatal
- Add `ClientConfig.ConfigureAnacrolixDhtServer`
- Add `PeerStorer` interface

## [v1.25.0] - 2021-02-09

- Limit conns per host across webseed clients
- Fix closing of webseed peers

## [v1.24.0] - 2021-02-05

- Expose `mmap_size` in sqlite storage, change default to 8 MiB
- Use locks on piece per resource pieces to prevent races
- Fix Close race in sqlite storage when batch writes disabled

## [v1.23.0] - 2021-02-02

- Fix sqlite storage for numconns 1
- Switch to reading consecutive incomplete chunks
- Fix stalls for responsive transfer tests

## [v1.22.0] - 2021-01-29

- Rework webseed peers to use a pool of requesters
- Remove requests as soon as chunk data is received
- Expose more callbacks and `Request`/`ChunkSpec` types
- Add sqlite-storage-cli
- Expose `Peer.Network` and `ReceivedUsefulData` callback
- Export `Peer`
- Implement `encoding.TextMarshaler` for `metainfo.Hash`

## [v1.21.0] - 2021-01-19

- Add `DropMutuallyCompletePeers` ClientConfig field
- Align webtorrent tracker to BEP-3
- Reannounce webtorrent webrtc offers on reconnect
- Fix panic on Ping/WriteMessage for webtorrent
- Fix boundary conditions trimming sqlite3 storage cache

## [v1.20.0] - 2021-01-04

- Add `mse.ReceiveHandshakeEx` for optimized handshakes
- Fix "none" event for WebTorrent announces
- Document `ClientConfig.DisableAcceptRateLimiting`
- Generalize internal string-limiter Key type

## [v1.19.2] - 2020-12-21

- Further fixes to webseed path encoding

## [v1.19.1] - 2020-12-21

- Add deprecated `ParseMagnetURI`

## [v1.19.0] - 2020-12-17

- sqlite storage: Add capacity management and batched writes
- Read from more than a single piece in each read to Torrent storage
- Performance improvements to PEX (`c1d189ed`)
- Mark piece complete without Client lock
- Read peer request data without Client lock
- Support `x.pe` magnet link parameter
- Add `ReceiveEncryptedHandshakeSkeys` callback
- Implement `fmt.Formatter` for `metainfo.Hash`
- sqlite storage: Buffer and batch writes with mmap support

## [v1.18.1] - 2020-10-28

- Fix peer request sleepiness

## [v1.18.0] - 2020-10-15

- Sanitize metainfo file paths for file-based storage
- Add sqlite data storage implementation (`d820f786`)
- Add `PeerConnClosed` callback
- Handle webseed HTTP response status codes
- Separate peer from PeerConn (continued from v1.16.0)

## [v1.17.1] - 2020-10-07

- Fix webseed requests for non-trivial path components

## [v1.17.0] - 2020-10-06

- Expose `Client.ConnStats`
- Include `ip` and `key` params in HTTP announces
- Limit half-open connections at the Client level
- Limit simultaneous announces to the same URL
- Optimize padding on `Piece` struct

## [v1.16.0] - 2020-09-03

Large release with **WebTorrent support** and **webseed** implementation.

- WebTorrent (webtorrent) support via pion/webrtc (#341)
- PEX: Share current connections with peers (BEP 11)
- Webseed (BEP 19) initial implementation
- Add per-torrent ability to disable uploading/downloading via `TorrentSpec`
- Add `DisableWebseeds` option
- Expose `Torrent.MergeSpec` for webseed/source additions
- Separate peer and PeerConn types (`02adc3f2`)
- Export `PeerImpl` and all its methods
- Abstract out segments mapping
- Switch CI to crawshaw.io/sqlite
- Add `ReadExtendedHandshake` and other client callbacks

## [v1.15.2] - 2020-04-28

- Disable keepalives for HTTP trackers (backport)

## [v1.15.1] - 2020-04-23

- Block updates to latest bazil.org/fuse
- Fix for anacrolix/log v0.7.0

## [v1.15.0] - 2020-03-24

- Support custom DHT servers
- Expose `PeerConn.PeerPieces`
- Differentiate between `storage.ClientImpl` and `ClientImplCloser`
- Add `Piece.State`
- Disable data downloading on storage write errors
- Make `io.EOF` an expected error from `storage.Piece.ReadAt`
- Expose `PieceStateRun` formatting

## [v1.14.0] - 2020-02-20

- Add support for non-IP-based networks
- Split Client dialers and listeners
- Extract the transfer tests

## [v1.13.0] - 2020-01-29

- Expose request strategies
- Restore the default duplicate request timeout strategy

## [v1.12.0] - 2020-01-23

- Resource per piece storage: Store incomplete chunks separately
- When piece checks fail, only ban untrusted peers
- Extract the request strategy logic (`4c989da2`)
- Propagate back piece hashing errors
- Disable accept rate limiting by default
- Fix various logging panics and races

## [v1.11.0] - 2020-01-03

- Add connection trust flag
- Possibility to change UPnP ID (#354)
- Use anacrolix/multiless for peer prioritization
- Proxy URL support with listener disabling

## [v1.10.0] - 2019-12-13

- Coalesce piece state change notifications on client unlock
- Use `path.utf8` first for some torrent (#350)
- Add `BytesCompleted` method for files (#347, #348)
- Export Peer function (#343)
- Export well known constants (#346)

## [v1.9.0] - 2019-11-07

- Move to anacrolix/stm and etcd-io/bbolt
- Use missinggo/v2/conntrack
- Rename peer source constants

## [v1.8.2] - 2019-10-11

- Don't close shared client piece completion in mmap storage
- Fix test for issue #335

## [v1.8.1] - 2019-10-04

- Fix logging panic in `BenchmarkConnectionMainReadLoop`

## [v1.8.0] - 2019-10-03

- Add `Client.String`
- Add `metainfo.Magnet.Params` for more open handling
- Prefix torrent logger message text
- Pass logger to DHTs
- Switch CI to go1.13

## [v1.7.1] - 2019-08-23

- Fix crash when receiving a request when we don't yet have the torrent info

## [v1.7.0] - 2019-08-21

- Restrict the number of concurrent piece hashes
- Upgrade to simplified logger
- Improve logging throughout
- Add `mse/cmd/mse`
- metainfo: Add fuzzing func

## [v1.6.0] - 2019-08-15

- Update all imports of dht to v2
- Don't include the handshake in the dial timeout for outgoing connections

## [v1.5.2] - 2019-07-30

- Fix race spewing Client stats
- `NewClient` nil `ClientConfig` should use dynamic port

## [v1.5.1] - 2019-07-26

- Fix tests on 32-bit architectures

## [v1.5.0] - 2019-07-25

- Rework header obfuscation with fallback support
- Try with the non-preferred header obfuscation if there's an error
- Ignore cached piece completion state when verifying data

## [v1.4.0] - 2019-07-17

- Fix announcing to S3 HTTP trackers
- Use fork of elgatito/upnp with go module files

## [v1.3.1] - 2019-07-17

- Send tracker stopped event from the tracker scraper routine

## [v1.3.0] - 2019-07-16

- bencode: Decode singleton lists of the expected type
- Improve `UnmarshalTypeError` string and list parsing error context
- Count client listener accepts

## [v1.2.0] - 2019-06-13

- Track concurrent chunk writes
- Add Started and Stopped events
- Add `OnQuery` hook
- Allow `ConnStats` being marshalled to JSON

## [v1.1.4] - 2019-04-24

- torrentfs: Fix directory listing basename extraction bug
- torrentfs: Fix ENOENT for root directory entries
- Check if peer ID exists

## [v1.1.3] - 2019-04-10

- Fix segfault on nil conntrack.EntryHandle when dialing
- When failing to read stored data, update completion state for the failed piece
- Make the default conntracker instance unlimited

## [v1.1.2] - 2019-03-29

- Fix leaked conntrack.EntryHandle

## [v1.1.1] - 2019-03-20

- Fix race condition in `Torrent.SetDisplayName`

## [v1.1.0] - 2019-03-19

- Switch entirely to anacrolix/log
- Panic on chunk write errors
- Add more flags to torrent-create
- Fix cancellation of DHT announce when peers are wanted
- Restart DHT announces at regular intervals

## [v1.0.1] - 2019-02-19

- Use a tagged version of anacrolix/log

## [v1.0.0] - 2019-01-08

Initial stable release.

[Unreleased]: https://github.com/anacrolix/torrent/compare/v1.61.0...HEAD
[v1.61.0]: https://github.com/anacrolix/torrent/compare/v1.60.0...v1.61.0
[v1.60.0]: https://github.com/anacrolix/torrent/compare/v1.59.1...v1.60.0
[v1.59.1]: https://github.com/anacrolix/torrent/compare/v1.59.0...v1.59.1
[v1.59.0]: https://github.com/anacrolix/torrent/compare/v1.58.1...v1.59.0
[v1.58.1]: https://github.com/anacrolix/torrent/compare/v1.58.0...v1.58.1
[v1.58.0]: https://github.com/anacrolix/torrent/compare/v1.57.1...v1.58.0
[v1.57.1]: https://github.com/anacrolix/torrent/compare/v1.57.0...v1.57.1
[v1.57.0]: https://github.com/anacrolix/torrent/compare/v1.56.1...v1.57.0
[v1.56.1]: https://github.com/anacrolix/torrent/compare/v1.56.0...v1.56.1
[v1.56.0]: https://github.com/anacrolix/torrent/compare/v1.55.0...v1.56.0
[v1.55.0]: https://github.com/anacrolix/torrent/compare/v1.54.1...v1.55.0
[v1.54.1]: https://github.com/anacrolix/torrent/compare/v1.54.0...v1.54.1
[v1.54.0]: https://github.com/anacrolix/torrent/compare/v1.53.3...v1.54.0
[v1.53.3]: https://github.com/anacrolix/torrent/compare/v1.53.2...v1.53.3
[v1.53.2]: https://github.com/anacrolix/torrent/compare/v1.53.1...v1.53.2
[v1.53.1]: https://github.com/anacrolix/torrent/compare/v1.53.0...v1.53.1
[v1.53.0]: https://github.com/anacrolix/torrent/compare/v1.52.8-beta...v1.53.0
[v1.52.8-beta]: https://github.com/anacrolix/torrent/compare/v1.52.7...v1.52.8-beta
[v1.52.7]: https://github.com/anacrolix/torrent/compare/v1.52.6...v1.52.7
[v1.52.6]: https://github.com/anacrolix/torrent/compare/v1.52.5...v1.52.6
[v1.52.5]: https://github.com/anacrolix/torrent/compare/v1.52.4...v1.52.5
[v1.52.4]: https://github.com/anacrolix/torrent/compare/v1.52.3...v1.52.4
[v1.52.3]: https://github.com/anacrolix/torrent/compare/v1.52.2...v1.52.3
[v1.52.2]: https://github.com/anacrolix/torrent/compare/v1.52.1...v1.52.2
[v1.52.1]: https://github.com/anacrolix/torrent/compare/v1.52.0...v1.52.1
[v1.52.0]: https://github.com/anacrolix/torrent/compare/v1.51.3...v1.52.0
[v1.51.3]: https://github.com/anacrolix/torrent/compare/v1.51.2...v1.51.3
[v1.51.2]: https://github.com/anacrolix/torrent/compare/v1.51.1...v1.51.2
[v1.51.1]: https://github.com/anacrolix/torrent/compare/v1.51.0...v1.51.1
[v1.51.0]: https://github.com/anacrolix/torrent/compare/v1.50.0...v1.51.0
[v1.50.0]: https://github.com/anacrolix/torrent/compare/v1.49.1...v1.50.0
[v1.49.1]: https://github.com/anacrolix/torrent/compare/v1.49.0...v1.49.1
[v1.49.0]: https://github.com/anacrolix/torrent/compare/v1.48.1...v1.49.0
[v1.48.1]: https://github.com/anacrolix/torrent/compare/v1.48.0...v1.48.1
[v1.48.0]: https://github.com/anacrolix/torrent/compare/v1.47.0...v1.48.0
[v1.47.0]: https://github.com/anacrolix/torrent/compare/v1.46.0...v1.47.0
[v1.46.0]: https://github.com/anacrolix/torrent/compare/v1.45.0...v1.46.0
[v1.45.0]: https://github.com/anacrolix/torrent/compare/v1.44.0...v1.45.0
[v1.44.0]: https://github.com/anacrolix/torrent/compare/v1.43.1...v1.44.0
[v1.43.1]: https://github.com/anacrolix/torrent/compare/v1.43.0...v1.43.1
[v1.43.0]: https://github.com/anacrolix/torrent/compare/v1.42.0...v1.43.0
[v1.42.0]: https://github.com/anacrolix/torrent/compare/v1.41.0...v1.42.0
[v1.41.0]: https://github.com/anacrolix/torrent/compare/v1.40.2...v1.41.0
[v1.40.2]: https://github.com/anacrolix/torrent/compare/v1.40.1...v1.40.2
[v1.40.1]: https://github.com/anacrolix/torrent/compare/v1.40.0...v1.40.1
[v1.40.0]: https://github.com/anacrolix/torrent/compare/v1.39.2...v1.40.0
[v1.39.2]: https://github.com/anacrolix/torrent/compare/v1.39.1...v1.39.2
[v1.39.1]: https://github.com/anacrolix/torrent/compare/v1.39.0...v1.39.1
[v1.39.0]: https://github.com/anacrolix/torrent/compare/v1.38.1...v1.39.0
[v1.38.1]: https://github.com/anacrolix/torrent/compare/v1.38.0...v1.38.1
[v1.38.0]: https://github.com/anacrolix/torrent/compare/v1.37.0...v1.38.0
[v1.37.0]: https://github.com/anacrolix/torrent/compare/v1.36.0...v1.37.0
[v1.36.0]: https://github.com/anacrolix/torrent/compare/v1.35.0...v1.36.0
[v1.35.0]: https://github.com/anacrolix/torrent/compare/v1.34.1...v1.35.0
[v1.34.1]: https://github.com/anacrolix/torrent/compare/v1.34.0...v1.34.1
[v1.34.0]: https://github.com/anacrolix/torrent/compare/v1.33.1...v1.34.0
[v1.33.1]: https://github.com/anacrolix/torrent/compare/v1.33.0...v1.33.1
[v1.33.0]: https://github.com/anacrolix/torrent/compare/v1.32.0...v1.33.0
[v1.32.0]: https://github.com/anacrolix/torrent/compare/v1.31.0...v1.32.0
[v1.31.0]: https://github.com/anacrolix/torrent/compare/v1.30.4...v1.31.0
[v1.30.4]: https://github.com/anacrolix/torrent/compare/v1.30.3...v1.30.4
[v1.30.3]: https://github.com/anacrolix/torrent/compare/v1.30.2...v1.30.3
[v1.30.2]: https://github.com/anacrolix/torrent/compare/v1.30.1...v1.30.2
[v1.30.1]: https://github.com/anacrolix/torrent/compare/v1.30.0...v1.30.1
[v1.30.0]: https://github.com/anacrolix/torrent/compare/v1.29.2...v1.30.0
[v1.29.2]: https://github.com/anacrolix/torrent/compare/v1.29.1...v1.29.2
[v1.29.1]: https://github.com/anacrolix/torrent/compare/v1.29.0...v1.29.1
[v1.29.0]: https://github.com/anacrolix/torrent/compare/v1.28.0...v1.29.0
[v1.28.0]: https://github.com/anacrolix/torrent/compare/v1.27.0...v1.28.0
[v1.27.0]: https://github.com/anacrolix/torrent/compare/v1.26.1...v1.27.0
[v1.26.1]: https://github.com/anacrolix/torrent/compare/v1.26.0...v1.26.1
[v1.26.0]: https://github.com/anacrolix/torrent/compare/v1.25.1...v1.26.0
[v1.25.1]: https://github.com/anacrolix/torrent/compare/v1.25.0...v1.25.1
[v1.25.0]: https://github.com/anacrolix/torrent/compare/v1.24.0...v1.25.0
[v1.24.0]: https://github.com/anacrolix/torrent/compare/v1.23.0...v1.24.0
[v1.23.0]: https://github.com/anacrolix/torrent/compare/v1.22.0...v1.23.0
[v1.22.0]: https://github.com/anacrolix/torrent/compare/v1.21.0...v1.22.0
[v1.21.0]: https://github.com/anacrolix/torrent/compare/v1.20.0...v1.21.0
[v1.20.0]: https://github.com/anacrolix/torrent/compare/v1.19.2...v1.20.0
[v1.19.2]: https://github.com/anacrolix/torrent/compare/v1.19.1...v1.19.2
[v1.19.1]: https://github.com/anacrolix/torrent/compare/v1.19.0...v1.19.1
[v1.19.0]: https://github.com/anacrolix/torrent/compare/v1.18.1...v1.19.0
[v1.18.1]: https://github.com/anacrolix/torrent/compare/v1.18.0...v1.18.1
[v1.18.0]: https://github.com/anacrolix/torrent/compare/v1.17.1...v1.18.0
[v1.17.1]: https://github.com/anacrolix/torrent/compare/v1.17.0...v1.17.1
[v1.17.0]: https://github.com/anacrolix/torrent/compare/v1.16.0...v1.17.0
[v1.16.0]: https://github.com/anacrolix/torrent/compare/v1.15.2...v1.16.0
[v1.15.2]: https://github.com/anacrolix/torrent/compare/v1.15.1...v1.15.2
[v1.15.1]: https://github.com/anacrolix/torrent/compare/v1.15.0...v1.15.1
[v1.15.0]: https://github.com/anacrolix/torrent/compare/v1.14.0...v1.15.0
[v1.14.0]: https://github.com/anacrolix/torrent/compare/v1.13.0...v1.14.0
[v1.13.0]: https://github.com/anacrolix/torrent/compare/v1.12.0...v1.13.0
[v1.12.0]: https://github.com/anacrolix/torrent/compare/v1.11.0...v1.12.0
[v1.11.0]: https://github.com/anacrolix/torrent/compare/v1.10.0...v1.11.0
[v1.10.0]: https://github.com/anacrolix/torrent/compare/v1.9.0...v1.10.0
[v1.9.0]: https://github.com/anacrolix/torrent/compare/v1.8.2...v1.9.0
[v1.8.2]: https://github.com/anacrolix/torrent/compare/v1.8.1...v1.8.2
[v1.8.1]: https://github.com/anacrolix/torrent/compare/v1.8.0...v1.8.1
[v1.8.0]: https://github.com/anacrolix/torrent/compare/v1.7.1...v1.8.0
[v1.7.1]: https://github.com/anacrolix/torrent/compare/v1.7.0...v1.7.1
[v1.7.0]: https://github.com/anacrolix/torrent/compare/v1.6.0...v1.7.0
[v1.6.0]: https://github.com/anacrolix/torrent/compare/v1.5.2...v1.6.0
[v1.5.2]: https://github.com/anacrolix/torrent/compare/v1.5.1...v1.5.2
[v1.5.1]: https://github.com/anacrolix/torrent/compare/v1.5.0...v1.5.1
[v1.5.0]: https://github.com/anacrolix/torrent/compare/v1.4.0...v1.5.0
[v1.4.0]: https://github.com/anacrolix/torrent/compare/v1.3.1...v1.4.0
[v1.3.1]: https://github.com/anacrolix/torrent/compare/v1.3.0...v1.3.1
[v1.3.0]: https://github.com/anacrolix/torrent/compare/v1.2.0...v1.3.0
[v1.2.0]: https://github.com/anacrolix/torrent/compare/v1.1.4...v1.2.0
[v1.1.4]: https://github.com/anacrolix/torrent/compare/v1.1.3...v1.1.4
[v1.1.3]: https://github.com/anacrolix/torrent/compare/v1.1.2...v1.1.3
[v1.1.2]: https://github.com/anacrolix/torrent/compare/v1.1.1...v1.1.2
[v1.1.1]: https://github.com/anacrolix/torrent/compare/v1.1.0...v1.1.1
[v1.1.0]: https://github.com/anacrolix/torrent/compare/v1.0.1...v1.1.0
[v1.0.1]: https://github.com/anacrolix/torrent/compare/v1.0.0...v1.0.1
[v1.0.0]: https://github.com/anacrolix/torrent/releases/tag/v1.0.0
