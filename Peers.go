package torrent

import "github.com/anacrolix/dht/krpc"

type Peers []Peer

func (me *Peers) FromPex(nas []krpc.NodeAddr, fs []pexPeerFlags) {
	for i, na := range nas {
		var p Peer
		var f pexPeerFlags
		if i < len(fs) {
			f = fs[i]
		}
		p.FromPex(na, f)
		*me = append(*me, p)
	}
}
