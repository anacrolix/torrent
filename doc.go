/*
Package torrent implements a torrent client. Goals include:
  - Configurable data storage, such as file, mmap, and piece-based.
  - Downloading on demand: [Reader] will request only the data required to
    satisfy Reads, which is ideal for streaming and torrentfs.

BitTorrent features implemented include:
  - Protocol encryption (MSE/PE)
  - DHT
  - uTP
  - PEX
  - WebTorrent (WebRTC-based peers)
  - WebSeeds (HTTP seeding)
  - BitTorrent v2 (hybrid torrents, merkle trees, piece layers)
  - Holepunching (NAT traversal)
  - Smart banning (detects and bans peers sending bad data)
  - UPnP port forwarding
  - Magnet links (v1 and v2)
  - IP blocklists
  - IPv6
  - HTTP and UDP tracker clients and servers
  - Tracker scraping
  - Upload and download rate limiting
  - FUSE filesystem (torrentfs)
  - BEPs:
  - 3: Basic BitTorrent protocol
  - 4: Known Number of Peers
  - 5: DHT
  - 6: Fast Extension
  - 7: IPv6 Tracker Extension
  - 9: ut_metadata
  - 10: Extension protocol
  - 11: PEX
  - 12: Multitracker metadata extension
  - 15: UDP Tracker Protocol
  - 19: WebSeed (HTTP/FTP Seeding)
  - 20: Peer ID convention ("-GTnnnn-")
  - 23: Tracker Returns Compact Peer Lists
  - 27: Private Torrents
  - 29: uTorrent transport protocol
  - 40: Canonical Peer Priority
  - 41: UDP Tracker Protocol Extensions
  - 42: DHT Security extension
  - 43: Read-only DHT Nodes
  - 47: Padding Files and Extended File Attributes
  - 52: BitTorrent v2
  - 54: Encoding metadata (UTF-8)
  - 55: Holepunch extension
*/
package torrent
