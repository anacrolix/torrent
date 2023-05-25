package torrent

import (
	"net/netip"
)

type clientHolepunchAddrSets struct {
	undialableWithoutHolepunch                            map[netip.AddrPort]struct{}
	undialableWithoutHolepunchDialedAfterHolepunchConnect map[netip.AddrPort]struct{}
	dialableOnlyAfterHolepunch                            map[netip.AddrPort]struct{}
	dialedSuccessfullyAfterHolepunchConnect               map[netip.AddrPort]struct{}
	probablyOnlyConnectedDueToHolepunch                   map[netip.AddrPort]struct{}
}

type ClientStats struct {
	ConnStats

	// Ongoing outgoing dial attempts. There may be more than one dial going on per peer address due
	// to hole-punch connect requests. The total may not match the sum of attempts for all Torrents
	// if a Torrent is dropped while there are outstanding dials.
	ActiveHalfOpenAttempts int

	NumPeersUndialableWithoutHolepunch int
	// Number of unique peer addresses that were dialed after receiving a holepunch connect message,
	// that have previously been undialable without any hole-punching attempts.
	NumPeersUndialableWithoutHolepunchDialedAfterHolepunchConnect int
	// Number of unique peer addresses that were successfully dialed and connected after a holepunch
	// connect message and previously failing to connect without holepunching.
	NumPeersDialableOnlyAfterHolepunch              int
	NumPeersDialedSuccessfullyAfterHolepunchConnect int
	NumPeersProbablyOnlyConnectedDueToHolepunch     int
}

func (cl *Client) statsLocked() (stats ClientStats) {
	stats.ConnStats = cl.connStats.Copy()
	stats.ActiveHalfOpenAttempts = cl.numHalfOpen

	stats.NumPeersUndialableWithoutHolepunch = len(cl.undialableWithoutHolepunch)
	stats.NumPeersUndialableWithoutHolepunchDialedAfterHolepunchConnect = len(cl.undialableWithoutHolepunchDialedAfterHolepunchConnect)
	stats.NumPeersDialableOnlyAfterHolepunch = len(cl.dialableOnlyAfterHolepunch)
	stats.NumPeersDialedSuccessfullyAfterHolepunchConnect = len(cl.dialedSuccessfullyAfterHolepunchConnect)
	stats.NumPeersProbablyOnlyConnectedDueToHolepunch = len(cl.probablyOnlyConnectedDueToHolepunch)

	return
}
