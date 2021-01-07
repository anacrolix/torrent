package torrent

import (
	"github.com/james-lawrence/torrent/dht/v2/krpc"

	"github.com/james-lawrence/torrent/btprotocol"
	"github.com/james-lawrence/torrent/tracker"
)

type Peers []Peer

func (me *Peers) AppendFromPex(nas []krpc.NodeAddr, fs []btprotocol.PexPeerFlags) {
	for i, na := range nas {
		var p Peer
		var f btprotocol.PexPeerFlags
		if i < len(fs) {
			f = fs[i]
		}
		p.FromPex(na, f)
		*me = append(*me, p)
	}
}

func (ret Peers) AppendFromTracker(ps []tracker.Peer) Peers {
	for _, p := range ps {
		_p := Peer{
			IP:     p.IP,
			Port:   p.Port,
			Source: peerSourceTracker,
		}
		copy(_p.ID[:], p.ID)
		ret = append(ret, _p)
	}
	return ret
}
