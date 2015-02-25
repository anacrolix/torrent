package torrent

import (
	"bitbucket.org/anacrolix/go.torrent/dht"
)

type Config struct {
	DataDir            string
	ListenAddr         string
	DisableTrackers    bool
	DownloadStrategy   DownloadStrategy
	NoDHT              bool
	DHTConfig          *dht.ServerConfig
	NoUpload           bool
	PeerID             string
	DisableUTP         bool
	DisableTCP         bool
	NoDefaultBlocklist bool
	// Defaults to "$HOME/.config/torrent"
	ConfigDir            string
	DisableMetainfoCache bool
	TorrentDataOpener
}
