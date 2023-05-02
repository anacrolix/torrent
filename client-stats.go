package torrent

type ClientStats struct {
	ConnStats

	// Ongoing outgoing dial attempts. There may be more than one dial going on per peer address due
	// to hole-punch connect requests. The total may not match the sum of attempts for all Torrents
	// if a Torrent is dropped while there are outstanding dials.
	ActiveHalfOpenAttempts int

	// Number of unique peer addresses that were dialed after receiving a holepunch connect message,
	// that have previously been undialable without any hole-punching attempts.
	NumPeersUndialableWithoutHolepunchDialedAfterHolepunchConnect int
	// Number of unique peer addresses that were successfully dialed and connected after a holepunch
	// connect message and previously failing to connect without holepunching.
	NumPeersDialableOnlyAfterHolepunch int
}
