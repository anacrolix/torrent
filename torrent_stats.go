package torrent

type TorrentStats struct {
	ConnStats // Aggregates stats over all connections past and present.

	// Ordered by expected descending quantities (if all is well).
	TotalPeers       int
	PendingPeers     int
	ActivePeers      int
	ConnectedSeeders int
	HalfOpenPeers    int
}
