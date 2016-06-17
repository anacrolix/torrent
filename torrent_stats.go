package torrent

type TorrentStats struct {
	ConnStats // Aggregates stats over all connections past and present.

	// total active DHT + PEX + LDP peers
	PeersCount int
	// How many times we've initiated a DHT announce.
	NumDHTAnnounces int
	// How many active peers we have from DHT source (currenly just last value from DHT request)
	PeersDHT int
	// error message
	ErrDHT error
	// last call
	LastAnnounceDHT int64
	// How many active peers we have from PEX source (currenly just last value from pex request)
	PeersPEX int
}
