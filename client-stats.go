package torrent

type ClientStats struct {
	ConnStats

	// Ongoing outgoing dial attempts. There may be more than one dial going on per peer address due
	// to hole-punch connect requests. The total may not match the sum of attempts for all Torrents
	// if a Torrent is dropped while there are outstanding dials.
	ActiveHalfOpenAttempts int
}
