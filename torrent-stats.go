package torrent

import (
	"reflect"
)

// Due to ConnStats, may require special alignment on some platforms. See
// https://github.com/anacrolix/torrent/issues/383.
type TorrentStats struct {
	AllConnStats
	TorrentStatCounters
	TorrentGauges
}

type AllConnStats struct {
	// Aggregates stats over all connections past and present. Some values may not have much meaning
	// in the aggregate context.
	ConnStats
	WebSeeds  ConnStats
	PeerConns ConnStats
}

func (me *AllConnStats) Copy() (ret AllConnStats) {
	ret.ConnStats = me.ConnStats.Copy()
	ret.WebSeeds = me.WebSeeds.Copy()
	ret.PeerConns = me.PeerConns.Copy()
	return
}

// Instantaneous metrics in Torrents, and aggregated for Clients.
type TorrentGauges struct {
	// Ordered by expected descending quantities (if all is well).
	TotalPeers       int
	PendingPeers     int
	ActivePeers      int
	ConnectedSeeders int
	HalfOpenPeers    int
	PiecesComplete   int
}

func (me *TorrentGauges) Add(agg TorrentGauges) {
	src := reflect.ValueOf(agg)
	dst := reflect.ValueOf(me).Elem()
	for i := 0; i < reflect.TypeFor[TorrentGauges]().NumField(); i++ {
		*dst.Field(i).Addr().Interface().(*int) += src.Field(i).Interface().(int)
	}
}

type TorrentStatCounters struct {
	BytesHashed Count
}
