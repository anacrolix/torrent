package torrent

import (
	"bitbucket.org/anacrolix/go.torrent/dht"
)

// Override Client defaults.
type Config struct {
	// Store torrent file data in this directory unless TorrentDataOpener is
	// specified.
	DataDir string
	// The address to listen for new uTP and TCP bittorrent protocol
	// connections. DHT shares a UDP socket with uTP unless configured
	// otherwise.
	ListenAddr string
	// Don't announce to trackers. This only leaves DHT to discover peers.
	DisableTrackers bool
	// Don't create a DHT.
	NoDHT bool
	// Overrides the default DHT configuration.
	DHTConfig *dht.ServerConfig
	// Don't chunks to peers.
	NoUpload bool
	// User-provided Client peer ID. If not present, one is generated automatically.
	PeerID string
	// For the bittorrent protocol.
	DisableUTP bool
	// For the bittorrent protocol.
	DisableTCP bool
	// Don't automatically load "$ConfigDir/blocklist".
	NoDefaultBlocklist bool
	// Defaults to "$HOME/.config/torrent". This is where "blocklist",
	// "torrents" and other operational files are stored.
	ConfigDir string
	// Don't save or load to a cache of torrent files stored in
	// "$ConfigDir/torrents".
	DisableMetainfoCache bool
	// Called to instantiate storage for each added torrent. Provided backends
	// are in $REPO/data. If not set, the "file" implementation is used.
	TorrentDataOpener
}
