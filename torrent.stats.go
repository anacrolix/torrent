package torrent

// Stats high level stats about the torrent.
type Stats struct {
	// Aggregates stats over all connections past and present. Some values may
	// not have much meaning in the aggregate context.
	ConnStats

	// metrics marking the progress of the torrent
	// these are in chunks.
	Missing     int
	Outstanding int
	Unverified  int
	Failed      int
	Completed   int

	// Ordered by expected descending quantities (if all is well).
	MaximumAllowedPeers int
	TotalPeers          int
	PendingPeers        int
	ActivePeers         int
	HalfOpenPeers       int
	// ConnectedSeeders    int

	Seeding bool
}
