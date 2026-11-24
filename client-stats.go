package torrent

import (
	"net/netip"

	g "github.com/anacrolix/generics"
)

func setAdd[K comparable](m *map[K]struct{}, elem K) {
	g.MakeMapIfNil(m)
	g.MapInsert(*m, elem, struct{}{})
}

type clientHolepunchAddrSets struct {
	undialableWithoutHolepunch                            map[netip.AddrPort]struct{}
	undialableWithoutHolepunchDialedAfterHolepunchConnect map[netip.AddrPort]struct{}
	dialableOnlyAfterHolepunch                            map[netip.AddrPort]struct{}
	dialedSuccessfullyAfterHolepunchConnect               map[netip.AddrPort]struct{}
	probablyOnlyConnectedDueToHolepunch                   map[netip.AddrPort]struct{}
	accepted                                              map[netip.AddrPort]struct{}
}

type ClientStats struct {
	TorrentStats

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

	// Turns waiting in lock domain actors now, summed over domains.
	LockDomainTurnsQueued int
	// The most turns that have waited at once in any one lock domain's actor.
	LockDomainTurnsQueuedMax int
	// The most actions any one lock domain has deferred to exclusive mode in a single hold.
	LockDomainExclusiveActionsMax int
}

func (cl *Client) statsLocked() (stats ClientStats) {
	stats.AllConnStats = cl.connStats.Copy()
	stats.TorrentStatCounters = copyCountFields(&cl.counters)
	for t := range cl.torrents {
		stats.TorrentGauges.Add(t.gaugesForClientStats())
	}
	cl.addLockDomainStats(&stats)

	stats.ActiveHalfOpenAttempts = cl.numHalfOpen

	stats.NumPeersUndialableWithoutHolepunch = len(cl.undialableWithoutHolepunch)
	stats.NumPeersUndialableWithoutHolepunchDialedAfterHolepunchConnect = len(cl.undialableWithoutHolepunchDialedAfterHolepunchConnect)
	stats.NumPeersDialableOnlyAfterHolepunch = len(cl.dialableOnlyAfterHolepunch)
	stats.NumPeersDialedSuccessfullyAfterHolepunchConnect = len(cl.dialedSuccessfullyAfterHolepunchConnect)
	stats.NumPeersProbablyOnlyConnectedDueToHolepunch = len(cl.probablyOnlyConnectedDueToHolepunch)

	return
}

// Reads the Torrent's gauges with the Client lock held in any mode. Outside exclusive mode, each
// domain is read locked in turn, so the totals across Torrents may not be from a single instant.
func (t *Torrent) gaugesForClientStats() TorrentGauges {
	if t.cl._mu.held {
		return t.gauges()
	}
	t.domain.mu.internal.RLock()
	defer t.domain.mu.internal.RUnlock()
	return t.gauges()
}
